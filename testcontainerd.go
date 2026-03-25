package testcontainerd

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/McHarvvvy/testcontainerd/client"
	tdconstant "github.com/McHarvvvy/testcontainerd/constant"
	"github.com/McHarvvvy/testcontainerd/container"
	"github.com/McHarvvvy/testcontainerd/daemon"
	"github.com/McHarvvvy/testcontainerd/tcdruntime"
)

// Runnable 表示可执行的测试入口。*testing.M 天然满足此接口。
type Runnable interface {
	Run() int
}

// RegisterContainersFunc 定义容器注册函数。
type RegisterContainersFunc func(ctx context.Context) ([]container.ContainerRegistration, error)

// StartSUTInput 定义被测服务启动输入。
type StartSUTInput = daemon.StartSUTInput

// SUTBootPlan 定义被测服务启动计划接口。
type SUTBootPlan = daemon.SUTBootPlan

// Config 定义 testcontainerd 的外部配置模型。
type Config struct {
	// Global 定义 daemon 与 client 的共享配置。
	Global GlobalConfig
	// Daemon 定义守护进程配置。
	Daemon DaemonConfig
	// Client 定义测试客户端配置。
	Client ClientConfig
	// SUT 定义被测服务启动计划。
	SUT SUTBootPlan
}

// GlobalConfig 定义 daemon 与 client 的共享配置。
type GlobalConfig struct {
	// Project 指定项目标识，用于 runtime 文件隔离。
	Project string
	// RuntimePath 指定共享 runtime 文件路径，空值时按 Project 自动推导。
	RuntimePath string
}

// DaemonConfig 定义守护进程配置。
type DaemonConfig struct {
	// Addr 指定 daemon 监听地址，默认建议使用 127.0.0.1:0。
	Addr string
	// Token 指定 daemon 鉴权令牌，空值时由 daemon 自动生成。
	Token string
	// IdleTTL 指定 daemon 空闲自动退出时间。
	IdleTTL time.Duration
}

// ClientConfig 定义客户端配置。
type ClientConfig struct {
	// HTTPTimeout 指定客户端请求 daemon 的 HTTP 超时时间。
	HTTPTimeout time.Duration
}

// TestContainerd 是 testcontainerd 的统一运行入口。
type TestContainerd struct {
	cfg                Config
	registerContainers RegisterContainersFunc
	registrations      []container.ContainerRegistration
}

// New 创建 TestContainerd 实例。
func New(cfg Config, registerContainers RegisterContainersFunc) (*TestContainerd, error) {
	normalizeGlobal(&cfg.Global)
	if strings.TrimSpace(cfg.Daemon.Addr) == "" {
		return nil, fmt.Errorf("daemon addr is required")
	}
	if cfg.Client.HTTPTimeout <= 0 {
		return nil, fmt.Errorf("client http timeout must be > 0")
	}
	if cfg.Daemon.IdleTTL <= 0 {
		return nil, fmt.Errorf("daemon idle ttl must be > 0")
	}
	if registerContainers == nil {
		return nil, fmt.Errorf("registerContainers cannot be nil")
	}

	regs, err := registerContainers(context.Background())
	if err != nil {
		return nil, err
	}
	if len(regs) == 0 {
		return nil, fmt.Errorf("container registrations cannot be empty")
	}
	seen := make(map[string]struct{}, len(regs))
	for i := range regs {
		name := strings.TrimSpace(regs[i].Name)
		if name == "" {
			return nil, fmt.Errorf("container registration name cannot be empty")
		}
		if regs[i].Start == nil {
			return nil, fmt.Errorf("container registration start cannot be nil: %s", name)
		}
		if _, ok := seen[name]; ok {
			return nil, fmt.Errorf("duplicated container name: %s", name)
		}
		seen[name] = struct{}{}
	}

	return &TestContainerd{
		cfg:                cfg,
		registerContainers: registerContainers,
		registrations:      regs,
	}, nil
}

func normalizeGlobal(cfg *GlobalConfig) {
	project := strings.TrimSpace(cfg.Project)
	if project == "" {
		project = "default"
	}
	runtimePath := strings.TrimSpace(cfg.RuntimePath)
	if runtimePath == "" {
		runtimePath = tcdruntime.DefaultRuntimePath(project)
	}
	cfg.Project = project
	cfg.RuntimePath = runtimePath
}

// RuntimePath 返回框架内部使用的 runtime 文件路径。
func (t *TestContainerd) RuntimePath() string {
	return t.cfg.Global.RuntimePath
}

// Run 是 TestMain 的统一执行入口。
func (t *TestContainerd) Run(m Runnable) int {
	if os.Getenv(tdconstant.EnvTCDMode) == tdconstant.TCDModeDaemon {
		return t.runDaemonMode()
	}
	return t.runClientMode(m)
}

func (t *TestContainerd) runDaemonMode() int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if t.registerContainers == nil {
		log.Printf("testcontainerd registerContainers is nil")
		return 1
	}
	if len(t.registrations) == 0 {
		var loadErr error
		t.registrations, loadErr = t.registerContainers(ctx)
		if loadErr != nil {
			log.Printf("testcontainerd register containers failed: %v", loadErr)
			return 1
		}
	}

	dcfg := daemon.Config{
		Addr:        t.cfg.Daemon.Addr,
		Token:       t.cfg.Daemon.Token,
		Project:     t.cfg.Global.Project,
		RuntimePath: t.cfg.Global.RuntimePath,
		IdleTTL:     t.cfg.Daemon.IdleTTL,
		SUT:         t.cfg.SUT,
	}
	if v := strings.TrimSpace(os.Getenv(tdconstant.EnvTCDRuntime)); v != "" {
		dcfg.RuntimePath = v
	}

	d, err := daemon.New(dcfg, t.registrations)
	if err != nil {
		log.Printf("testcontainerd daemon.New failed: %v", err)
		return 1
	}
	if err = d.Start(ctx); err != nil {
		log.Printf("testcontainerd daemon mode exited with error: %v", err)
		return 1
	}
	return 0
}

func (t *TestContainerd) runClientMode(m Runnable) int {
	if m == nil {
		return 1
	}
	ctx := context.Background()
	c, err := client.New(ctx, client.Config{
		Project:     t.cfg.Global.Project,
		RuntimePath: t.cfg.Global.RuntimePath,
		HTTPTimeout: t.cfg.Client.HTTPTimeout,
	})
	if err != nil {
		log.Printf("testcontainerd client.New failed: %v", err)
		return 1
	}

	lease, err := c.Acquire(ctx)
	if err != nil {
		log.Printf("testcontainerd acquire failed: %v", err)
		return 1
	}
	stopHB := c.StartHeartbeat(lease.LeaseID)

	code := m.Run()
	// 关键决策：先停止心跳再释放租约，避免 release 阻塞期间继续续租导致 SUT 迟迟不回收。
	stopHB()
	releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = c.Release(releaseCtx, lease.LeaseID, code)
	return code
}
