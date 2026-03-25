package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tdconstant "github.com/McHarvvvy/testcontainerd/constant"
	"github.com/McHarvvvy/testcontainerd/container"
	"github.com/McHarvvvy/testcontainerd/protocol"
	"github.com/McHarvvvy/testcontainerd/tcdruntime"
)

// Config 表示守护进程配置。
type Config struct {
	// Addr 指定 daemon 监听地址，支持 127.0.0.1:0 自动分配端口。
	Addr string
	// Token 指定 daemon 鉴权令牌，客户端请求需携带相同值。
	Token string
	// Project 用于标识当前测试项目，便于日志与运行时隔离。
	Project string
	// RuntimePath 指定 runtime 发现文件路径，供客户端发现 daemon。
	RuntimePath string
	// IdleTTL 指定 daemon 在无活跃租约时的自动退出时间。
	IdleTTL time.Duration
	// SUT 定义被测服务启动计划与生命周期策略。
	SUT SUTBootPlan
}

// Daemon 负责管理 testcontainer 生命周期。
type Daemon struct {
	cfg         Config
	bundle      *container.Bundle
	store       *leaseStore
	httpServer  *http.Server
	startedAt   time.Time
	runtimePath string
	sut         *sutManager

	startMu      sync.Mutex
	infraStarted bool

	shutdownOnce sync.Once
	done         chan struct{}

	// acquireInFlight 用于抑制空闲回收误判：
	// 请求还在执行中时，即使 active lease 为 0 也不能触发退出。
	acquireInFlight atomic.Int32
}

// New 创建守护进程实例。
func New(cfg Config, registry *container.Registry) *Daemon {
	if cfg.Addr == "" {
		// 关键决策：默认使用随机可用端口，避免本地并发测试或残留进程导致固定端口冲突。
		cfg.Addr = "127.0.0.1:0"
	}
	if cfg.Token == "" {
		cfg.Token = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	if cfg.IdleTTL <= 0 {
		cfg.IdleTTL = 15 * time.Second
	}
	if cfg.RuntimePath == "" {
		cfg.RuntimePath = tcdruntime.DefaultRuntimePath("default")
	}
	if strings.TrimSpace(cfg.Project) == "" {
		cfg.Project = "default"
	}
	if registry == nil {
		registry = container.NewRegistry()
	}
	registry.Freeze()
	return &Daemon{
		cfg:         cfg,
		bundle:      container.NewBundle(registry),
		store:       newLeaseStore(),
		startedAt:   time.Now(),
		runtimePath: cfg.RuntimePath,
		sut:         newSUTManager(cfg.SUT),
		done:        make(chan struct{}),
	}
}

// Start 启动守护进程并阻塞直到退出。
func (d *Daemon) Start(ctx context.Context) error {
	logFile, err := tcdruntime.OpenDaemonLogFile(d.runtimePath)
	if err != nil {
		return err
	}
	defer logFile.Close()
	log.SetOutput(logFile)

	if err := CleanupOrphans(ctx, d.bundle.RegisteredContainers()); err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc(protocol.PathAcquire, d.withAuth(d.handleAcquire))
	mux.HandleFunc(protocol.PathHeartbeat, d.withAuth(d.handleHeartbeat))
	mux.HandleFunc(protocol.PathRelease, d.withAuth(d.handleRelease))
	mux.HandleFunc(protocol.PathState, d.withAuth(d.handleState))

	ln, err := net.Listen(tdconstant.ProtocolTCP, d.cfg.Addr)
	if err != nil {
		return err
	}
	d.httpServer = &http.Server{Handler: mux}

	info := protocol.RuntimeInfo{
		Addr:      ln.Addr().String(),
		Token:     d.cfg.Token,
		PID:       os.Getpid(),
		StartedAt: d.startedAt,
	}
	if err = tcdruntime.Write(d.runtimePath, info); err != nil {
		_ = ln.Close()
		return err
	}
	// 关键决策：无论是正常关闭还是 Serve 异常返回，都在退出路径兜底清理 runtime 文件。
	// shutdownServer 中也会执行同样清理，这里再次兜底可覆盖异常分支并保持幂等。
	defer tcdruntime.Remove(d.runtimePath)

	go d.loopReaper()
	go func() {
		<-ctx.Done()
		d.shutdownServer(context.Background())
	}()

	err = d.httpServer.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (d *Daemon) withAuth(next func(http.ResponseWriter, *http.Request)) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		// daemon 仅面向本机测试进程，但仍通过 token 做最小隔离，
		// 防止同机其它进程误调用导致租约状态错乱。
		token := strings.TrimSpace(r.Header.Get(protocol.HeaderToken))
		if token == "" || token != d.cfg.Token {
			writeError(w, http.StatusUnauthorized, protocol.ErrCodeUnauthorized, "unauthorized")
			return
		}
		next(w, r)
	}
}

func (d *Daemon) handleAcquire(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, protocol.ErrCodeBadRequest, "invalid method")
		return
	}
	d.acquireInFlight.Add(1)
	defer d.acquireInFlight.Add(-1)
	var req protocol.AcquireReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.Printf("testcontainerd acquire decode request failed: %v", err)
		writeError(w, http.StatusBadRequest, protocol.ErrCodeBadRequest, err.Error())
		return
	}
	// 关键决策：Acquire 串联“容器基础设施就绪 + 被测服务进程就绪”，
	// 只有两者都完成才返回租约，确保用例拿到的是可用环境而非半初始化状态。
	infraStart := time.Now()
	if err := d.ensureInfraStarted(r.Context()); err != nil {
		log.Printf("testcontainerd acquire ensure infra failed cost=%s err=%v", time.Since(infraStart), err)
		writeError(w, http.StatusInternalServerError, protocol.ErrCodeInternal, err.Error())
		return
	}
	appStart := time.Now()
	if err := d.ensureSUTStarted(r.Context()); err != nil {
		log.Printf("testcontainerd acquire ensure sut failed cost=%s err=%v", time.Since(appStart), err)
		writeError(w, http.StatusInternalServerError, protocol.ErrCodeInternal, err.Error())
		return
	}
	l := d.store.acquire(req.PID, tdconstant.LeaseTTL)
	writeJSON(w, http.StatusOK, protocol.AcquireResp{
		LeaseID:    l.ID,
		AcquiredAt: time.Now(),
	})
}

func (d *Daemon) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, protocol.ErrCodeBadRequest, "invalid method")
		return
	}
	var req protocol.HeartbeatReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, protocol.ErrCodeBadRequest, err.Error())
		return
	}
	if ok := d.store.heartbeat(req.LeaseID, tdconstant.LeaseTTL); !ok {
		writeError(w, http.StatusNotFound, protocol.ErrCodeNotFound, "lease not found")
		return
	}
	writeJSON(w, http.StatusOK, protocol.OKResp{OK: true})
}

func (d *Daemon) handleRelease(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, protocol.ErrCodeBadRequest, "invalid method")
		return
	}
	var req protocol.ReleaseReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, protocol.ErrCodeBadRequest, err.Error())
		return
	}
	if ok := d.store.release(req.LeaseID); !ok {
		writeError(w, http.StatusNotFound, protocol.ErrCodeNotFound, "lease not found")
		return
	}
	writeJSON(w, http.StatusOK, protocol.OKResp{OK: true})
}

func (d *Daemon) handleState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, protocol.ErrCodeBadRequest, "invalid method")
		return
	}
	active := d.store.activeCount(time.Now())
	sutState := d.sut.snapshot()
	state := protocol.StateResp{
		InfraReady:    d.bundle.Started(),
		ActiveLeases:  active,
		Containers:    d.bundle.Containers(),
		StartedAt:     d.startedAt,
		IdleTTLSecond: int(d.cfg.IdleTTL.Seconds()),
		AppReady:      sutState.Ready,
		AppPID:        sutState.PID,
		AppStartedAt:  sutState.StartedAt,
		AppRestarts:   sutState.RestartCount,
		AppLastError:  sutState.LastError,
	}
	writeJSON(w, http.StatusOK, state)
}

func (d *Daemon) loopReaper() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	var idleSince time.Time
	var sutIdleSince time.Time
	for {
		select {
		case <-d.done:
			return
		case <-ticker.C:
			now := time.Now()
			d.store.gc(now)
			active := d.store.activeCount(now)
			if active > 0 {
				// 只要有活跃租约，两个空闲计时器都要重置，
				// 否则会在边界时刻误触发提前回收。
				idleSince = time.Time{}
				sutIdleSince = time.Time{}
				continue
			}
			if d.acquireInFlight.Load() > 0 {
				// Acquire 尚未落租约前也视为“忙碌期”，
				// 防止刚启动完 infra/sut 就被 reaper 抢先停掉。
				idleSince = time.Time{}
				sutIdleSince = time.Time{}
				continue
			}
			// 关键决策：SUT 与 daemon 使用双层空闲策略，
			// 先按 SUT IdleTTL 回收被测服务，再按 daemon IdleTTL 回收容器与进程本体。
			if sutIdleSince.IsZero() {
				sutIdleSince = now
			} else if now.Sub(sutIdleSince) >= d.sut.idleTTL() {
				_ = d.stopSUTOnly(context.Background())
			}
			// 关键决策：这里不依赖 bundle.Started()。
			// 即使 infra 尚未成功启动，空闲 daemon 也要能按 IdleTTL 自动退出，避免并发竞争产生孤儿 runner。
			if idleSince.IsZero() {
				idleSince = now
				continue
			}
			if now.Sub(idleSince) >= d.cfg.IdleTTL {
				d.shutdownServer(context.Background())
				return
			}
		}
	}
}

func (d *Daemon) shutdownServer(ctx context.Context) {
	d.shutdownOnce.Do(func() {
		close(d.done)
		// 先停 SUT 再停容器：
		// 避免 SUT 在依赖容器先下线后出现大量无意义报错。
		_ = d.stopSUTOnly(ctx)
		_ = d.stopContainersOnly(ctx)
		if d.httpServer != nil {
			shutdownCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			_ = d.httpServer.Shutdown(shutdownCtx)
		}
		tcdruntime.Remove(d.runtimePath)
	})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, protocol.ErrorResp{Code: code, Message: message})
}
