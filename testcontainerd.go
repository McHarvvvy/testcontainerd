package testcontainerd

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/McHarvvvy/testcontainerd/client"
	tdconstant "github.com/McHarvvvy/testcontainerd/constant"
	"github.com/McHarvvvy/testcontainerd/container"
	"github.com/McHarvvvy/testcontainerd/daemon"
	"github.com/McHarvvvy/testcontainerd/tcdruntime"
)

// Registrar 定义容器注册行为。
type Registrar interface {
	Register(cfg container.InstanceConfig) error
}

// RegisterContainersHook 定义容器注册钩子。
type RegisterContainersHook func(ctx context.Context, r Registrar) error

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
	cfg          Config
	registerHook RegisterContainersHook
	registry     *container.Registry
}

type registryRegistrar struct {
	reg *container.Registry
}

func (r *registryRegistrar) Register(cfg container.InstanceConfig) error {
	return r.reg.Register(cfg)
}

// New 创建 TestContainerd 实例。
func New(cfg Config, registerHook RegisterContainersHook) (*TestContainerd, error) {
	normalizeGlobal(&cfg.Global)
	if cfg.Client.HTTPTimeout <= 0 {
		cfg.Client.HTTPTimeout = 1 * time.Minute
	}
	if cfg.Daemon.IdleTTL <= 0 {
		cfg.Daemon.IdleTTL = 60 * time.Second
	}

	return &TestContainerd{
		cfg:          cfg,
		registerHook: registerHook,
		registry:     container.NewRegistry(),
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

// Run 是 TestMain 的统一执行入口。
func (t *TestContainerd) Run(m *testing.M) int {
	if os.Getenv(tdconstant.EnvTCDMode) == tdconstant.TCDModeDaemon {
		return t.runDaemonMode()
	}
	return t.runClientMode(m)
}

func (t *TestContainerd) runDaemonMode() int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if t.registerHook != nil {
		if err := t.registerHook(ctx, &registryRegistrar{reg: t.registry}); err != nil {
			log.Printf("testcontainerd register hook failed: %v", err)
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

	d := daemon.New(dcfg, t.registry)
	if err := d.Start(ctx); err != nil {
		log.Printf("testcontainerd daemon mode exited with error: %v", err)
		return 1
	}
	return 0
}

func (t *TestContainerd) runClientMode(m *testing.M) int {
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
	if t.cfg.SUT != nil && t.cfg.SUT.IsEnable() {
		// 关键决策：endpoint 环境变量由业务方计划对象统一注入，
		// 避免 testcontainerd 框架层写死具体环境变量名称。
		if err = t.cfg.SUT.SetEnvEndpoint(); err != nil {
			log.Printf("testcontainerd set sut endpoint env failed: %v", err)
			_ = c.Release(ctx, lease.LeaseID, 1)
			return 1
		}
	}

	code := m.Run()
	// 关键决策：先停止心跳再释放租约，避免 release 阻塞期间继续续租导致 SUT 迟迟不回收。
	stopHB()
	releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = c.Release(releaseCtx, lease.LeaseID, code)
	return code
}
