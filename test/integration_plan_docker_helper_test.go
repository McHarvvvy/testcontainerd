//go:build integration_plan

package test

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	tcd "github.com/McHarvvvy/testcontainerd"
	"github.com/McHarvvvy/testcontainerd/container"
	"github.com/McHarvvvy/testcontainerd/tcdruntime"
	dockercontainer "github.com/docker/docker/api/types/container"
	dockerclient "github.com/docker/docker/client"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// ===========================================================================
// TestMain — 测试入口，处理 daemon 子进程 re-exec
// ===========================================================================

func TestMain(m *testing.M) {
	if os.Getenv("TCD_MODE") == "daemon" {
		os.Exit(handleDaemonMode())
	}
	os.Exit(m.Run())
}

// handleDaemonMode 在 daemon 子进程中根据 TCD_SCENARIO 调用对应的配置函数并启动 daemon。
// 每个场景配置函数与测试用例函数共用同一套配置，确保 client 与 daemon 使用完全相同的数据。
func handleDaemonMode() int {
	runtimePath := os.Getenv("TCD_RUNTIME")
	scenario := os.Getenv("TCD_SCENARIO")

	var cfg tcd.Config
	var registerFn tcd.RegisterContainersFunc

	switch scenario {
	case "TestTC09SequentialRunReusesSameContainer":
		cfg, registerFn = tc09Config(runtimePath)
	case "TestTC10PartialStartFailureRollsBack":
		cfg, registerFn = tc10Config(runtimePath)
	case "TestTC11InitFailureRollsBackAll":
		cfg, registerFn = tc11Config(runtimePath)
	case "TestTC14SUTEnvInjectedIntoSUTProcess":
		cfg, registerFn = tc14Config(runtimePath)
	case "TestTC15SUTProbeReadyBeforeFakeMRun":
		cfg, registerFn = tc15Config(runtimePath)
	case "TestTC16DaemonIdleExitCleansUp":
		cfg, registerFn = tc16Config(runtimePath)
	case "TestTC17ConcurrentRunReusesSameContainer":
		cfg, registerFn = tc17Config(runtimePath)
	default:
		cfg = tcd.Config{
			Global: tcd.GlobalConfig{RuntimePath: runtimePath},
			Daemon: tcd.DaemonConfig{Addr: "127.0.0.1:0", IdleTTL: 5 * time.Second},
			Client: tcd.ClientConfig{HTTPTimeout: 15 * time.Second},
		}
		registerFn = singleRedisRegisterFunc("redis")
	}

	inst, err := tcd.New(cfg, registerFn)
	if err != nil {
		log.Printf("daemon mode: tcd.New failed: %v", err)
		return 1
	}
	return inst.Run(&fakeRunnable{run: func() int { return 0 }})
}

// ===========================================================================
// 场景配置函数 — 测试函数与 TestMain switch case 调用同一函数，保证配置一致
// ===========================================================================

func tc09Config(runtimePath string) (tcd.Config, tcd.RegisterContainersFunc) {
	return tcd.Config{
		Global: tcd.GlobalConfig{RuntimePath: runtimePath},
		Daemon: tcd.DaemonConfig{Addr: "127.0.0.1:0", IdleTTL: 15 * time.Second},
		Client: tcd.ClientConfig{HTTPTimeout: 15 * time.Second},
	}, singleRedisRegisterFunc("redis")
}

func tc10Config(runtimePath string) (tcd.Config, tcd.RegisterContainersFunc) {
	return tcd.Config{
		Global: tcd.GlobalConfig{RuntimePath: runtimePath},
		Daemon: tcd.DaemonConfig{Addr: "127.0.0.1:0", IdleTTL: 5 * time.Second},
		Client: tcd.ClientConfig{HTTPTimeout: 15 * time.Second},
	}, func(ctx context.Context) ([]container.ContainerRegistration, error) {
		return []container.ContainerRegistration{
			{Name: "redis-good", Start: startRealRedis("redis-good")},
			{Name: "redis-bad", Start: startBadImage("redis-bad")},
		}, nil
	}
}

func tc11Config(runtimePath string) (tcd.Config, tcd.RegisterContainersFunc) {
	return tcd.Config{
		Global: tcd.GlobalConfig{RuntimePath: runtimePath},
		Daemon: tcd.DaemonConfig{Addr: "127.0.0.1:0", IdleTTL: 5 * time.Second},
		Client: tcd.ClientConfig{HTTPTimeout: 15 * time.Second},
	}, func(ctx context.Context) ([]container.ContainerRegistration, error) {
		return []container.ContainerRegistration{
			{Name: "redis-a", Start: startRealRedis("redis-a")},
			{
				Name:  "redis-b",
				Start: startRealRedis("redis-b"),
				Init:  func(ctx context.Context) error { return fmt.Errorf("init failed: seed data error") },
			},
		}, nil
	}
}

func tc14Config(runtimePath string) (tcd.Config, tcd.RegisterContainersFunc) {
	// envFile 从 runtimePath 推导，test 与 daemon 调用同一函数得到相同路径。
	// GetCommand 将 s.envFile 注入 SUT 进程，test 函数从 cfg.SUT 读取，无需额外 env var。
	envFile := filepath.Join(filepath.Dir(runtimePath), "sut_env_output.txt")
	return tcd.Config{
		Global: tcd.GlobalConfig{RuntimePath: runtimePath},
		Daemon: tcd.DaemonConfig{Addr: "127.0.0.1:0", IdleTTL: 5 * time.Second},
		Client: tcd.ClientConfig{HTTPTimeout: 15 * time.Second},
		SUT:    sutEnvVerifySUT{envFile: envFile},
	}, singleRedisRegisterFunc("redis")
}

// tc15ProbeAddr 根据 runtimePath 用 FNV-32a 哈希确定性推导探测端口（范围 20000–59999）。
// t.TempDir() 保证每次测试的 runtimePath 唯一，因此端口唯一，无需 env var 传递。
func tc15ProbeAddr(runtimePath string) string {
	var h uint32 = 2166136261
	for _, b := range []byte(runtimePath) {
		h ^= uint32(b)
		h *= 16777619
	}
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(int(20000+h%40000)))
}

func tc15Config(runtimePath string) (tcd.Config, tcd.RegisterContainersFunc) {
	return tcd.Config{
		Global: tcd.GlobalConfig{RuntimePath: runtimePath},
		Daemon: tcd.DaemonConfig{Addr: "127.0.0.1:0", IdleTTL: 5 * time.Second},
		Client: tcd.ClientConfig{HTTPTimeout: 15 * time.Second},
		SUT:    delayedProbeSUT{probeAddr: tc15ProbeAddr(runtimePath)},
	}, singleRedisRegisterFunc("redis")
}

func tc16Config(runtimePath string) (tcd.Config, tcd.RegisterContainersFunc) {
	return tcd.Config{
		Global: tcd.GlobalConfig{RuntimePath: runtimePath},
		Daemon: tcd.DaemonConfig{Addr: "127.0.0.1:0", IdleTTL: 1 * time.Second},
		Client: tcd.ClientConfig{HTTPTimeout: 15 * time.Second},
	}, singleRedisRegisterFunc("redis")
}

func tc17Config(runtimePath string) (tcd.Config, tcd.RegisterContainersFunc) {
	return tcd.Config{
		Global: tcd.GlobalConfig{RuntimePath: runtimePath},
		Daemon: tcd.DaemonConfig{Addr: "127.0.0.1:0", IdleTTL: 15 * time.Second},
		Client: tcd.ClientConfig{HTTPTimeout: 15 * time.Second},
	}, singleRedisRegisterFunc("redis")
}

// ===========================================================================
// fakeRunnable — 测试用 Runnable 实现
// ===========================================================================

type fakeRunnable struct {
	run func() int
}

func (f *fakeRunnable) Run() int { return f.run() }

// ===========================================================================
// SUTBootPlan 实现
// ===========================================================================

// delayedProbeSUT — TC-15: 模拟延迟启动后监听探测端口的 SUT。
type delayedProbeSUT struct {
	probeAddr string
}

func (d delayedProbeSUT) IsEnable() bool                 { return true }
func (d delayedProbeSUT) GetIdleTTL() time.Duration      { return 2 * time.Second }
func (d delayedProbeSUT) GetReadyTimeout() time.Duration { return 10 * time.Second }
func (d delayedProbeSUT) GetGracePeriod() time.Duration  { return 2 * time.Second }
func (d delayedProbeSUT) GetProbeAddrs() []string        { return []string{d.probeAddr} }
func (d delayedProbeSUT) GetCommand(_ context.Context, _ tcd.StartSUTInput) (*exec.Cmd, error) {
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperDelayedProbeSUT")
	cmd.Env = append(os.Environ(), "TCD_MODE=", "TCD_HELPER_PROCESS=1", "TCD_HELPER_PROBE_ADDR="+d.probeAddr)
	cmd.Dir = os.TempDir()
	return cmd, nil
}

var _ tcd.SUTBootPlan = delayedProbeSUT{}

// sutEnvVerifySUT — TC-14: 调用方在 GetCommand 中通过 Docker API 查询 Redis 地址并设入 cmd.Env，
// SUT helper 进程 ping Redis 后写 "success"/"fail" 到 envFile。
type sutEnvVerifySUT struct {
	envFile string
}

func (s sutEnvVerifySUT) IsEnable() bool                 { return true }
func (s sutEnvVerifySUT) GetIdleTTL() time.Duration      { return 2 * time.Second }
func (s sutEnvVerifySUT) GetReadyTimeout() time.Duration { return 10 * time.Second }
func (s sutEnvVerifySUT) GetGracePeriod() time.Duration  { return 2 * time.Second }
func (s sutEnvVerifySUT) GetProbeAddrs() []string        { return nil } // 无探测端口，用 alive check
func (s sutEnvVerifySUT) GetCommand(ctx context.Context, _ tcd.StartSUTInput) (*exec.Cmd, error) {
	// 调用方全权负责：通过 Docker API 查询 Redis 地址，设入 cmd.Env
	redisAddr, err := redisAddressByContainerName(ctx, "redis")
	if err != nil {
		return nil, fmt.Errorf("get redis address for SUT: %w", err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperSUTEnvVerify")
	cmd.Env = append(os.Environ(),
		"TCD_MODE=", // 清除 daemon 模式标记，让 helper 进程正常执行测试函数
		"TCD_HELPER_PROCESS=1",
		"TCD_HELPER_ENV_FILE="+s.envFile,
		"TEST_REDIS_ADDR="+redisAddr,
	)
	cmd.Dir = os.TempDir()
	return cmd, nil
}

var _ tcd.SUTBootPlan = sutEnvVerifySUT{}

// ===========================================================================
// Test helper 进程（通过 re-exec 模式运行）
// ===========================================================================

// TestHelperDelayedProbeSUT — TC-15 的 SUT helper 进程：延迟后监听探测端口。
func TestHelperDelayedProbeSUT(t *testing.T) {
	if os.Getenv("TCD_HELPER_PROCESS") != "1" {
		return
	}
	addr := strings.TrimSpace(os.Getenv("TCD_HELPER_PROBE_ADDR"))
	if addr == "" {
		os.Exit(2)
	}
	time.Sleep(300 * time.Millisecond)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		os.Exit(3)
	}
	defer ln.Close()
	for {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			return
		}
		_ = conn.Close()
	}
}

// TestHelperSUTEnvVerify — TC-14 的 SUT helper 进程：
// 读取 TEST_REDIS_ADDR，执行 Redis GET 命令，写 "success"/"fail" 到 envFile，阻塞保活。
func TestHelperSUTEnvVerify(t *testing.T) {
	if os.Getenv("TCD_HELPER_PROCESS") != "1" {
		return
	}
	envFile := strings.TrimSpace(os.Getenv("TCD_HELPER_ENV_FILE"))
	if envFile == "" {
		os.Exit(2)
	}
	redisAddr := os.Getenv("TEST_REDIS_ADDR")
	if redisAddr == "" {
		_ = os.WriteFile(envFile, []byte("fail"), 0o644)
		blockForever() // 阻塞保活
	}
	if err := getRedis(redisAddr); err != nil {
		_ = os.WriteFile(envFile, []byte("fail"), 0o644)
	} else {
		_ = os.WriteFile(envFile, []byte("success"), 0o644)
	}
	blockForever() // 阻塞保活，框架在测试结束后终止此进程
}

// ===========================================================================
// 辅助函数：配置构造
// ===========================================================================

func dockerTestConfig(t *testing.T) tcd.Config {
	t.Helper()
	return tcd.Config{
		Global: tcd.GlobalConfig{
			Project:     fmt.Sprintf("test-%d", time.Now().UnixNano()),
			RuntimePath: filepath.Join(t.TempDir(), "runtime.json"),
		},
		Daemon: tcd.DaemonConfig{Addr: "127.0.0.1:0", IdleTTL: 5 * time.Second},
		Client: tcd.ClientConfig{HTTPTimeout: 15 * time.Second},
	}
}

func singleRedisRegisterFunc(name string) tcd.RegisterContainersFunc {
	return func(ctx context.Context) ([]container.ContainerRegistration, error) {
		return []container.ContainerRegistration{
			{Name: name, Start: startRealRedis(name)},
		}, nil
	}
}

// ===========================================================================
// 辅助函数：容器 Start 工厂
// ===========================================================================

func startRealRedis(name string) func(ctx context.Context) (testcontainers.Container, error) {
	return func(ctx context.Context) (testcontainers.Container, error) {
		ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: testcontainers.ContainerRequest{
				Name:         name,
				Image:        "redis:7.2-alpine",
				ExposedPorts: []string{"6379/tcp"},
				WaitingFor:   wait.ForListeningPort("6379/tcp"),
			},
			Started: true,
		})
		if err != nil {
			return nil, err
		}
		return ctr, nil
	}
}

func startBadImage(name string) func(ctx context.Context) (testcontainers.Container, error) {
	return func(ctx context.Context) (testcontainers.Container, error) {
		ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: testcontainers.ContainerRequest{
				Name:         name,
				Image:        "redis:non-existent-tag-for-testcontainerd",
				ExposedPorts: []string{"6379/tcp"},
				WaitingFor:   wait.ForListeningPort("6379/tcp"),
			},
			Started: true,
		})
		if err != nil {
			return nil, err
		}
		return ctr, nil
	}
}

// ===========================================================================
// 辅助函数：Docker 操作
// ===========================================================================

func requireDocker(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		t.Skipf("docker client unavailable: %v", err)
	}
	defer cli.Close()
	if _, err = cli.Ping(ctx); err != nil {
		t.Skipf("docker daemon unavailable: %v", err)
	}
}

func containerExists(ctx context.Context, name string) (bool, error) {
	cli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return false, err
	}
	defer cli.Close()
	_, err = cli.ContainerInspect(ctx, name)
	if err == nil {
		return true, nil
	}
	if strings.Contains(strings.ToLower(err.Error()), "no such container") || strings.Contains(err.Error(), "not found") {
		return false, nil
	}
	return false, err
}

func containerIDByName(ctx context.Context, name string) (string, error) {
	cli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return "", err
	}
	defer cli.Close()
	inspect, err := cli.ContainerInspect(ctx, name)
	if err != nil {
		return "", err
	}
	return inspect.ID, nil
}

func redisAddressByContainerName(ctx context.Context, name string) (string, error) {
	cli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return "", err
	}
	defer cli.Close()
	inspect, err := cli.ContainerInspect(ctx, name)
	if err != nil {
		return "", err
	}
	pb, ok := inspect.NetworkSettings.Ports["6379/tcp"]
	if !ok || len(pb) == 0 {
		return "", fmt.Errorf("redis mapped port not found")
	}
	host := pb[0].HostIP
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, pb[0].HostPort), nil
}

func cleanupContainerByName(ctx context.Context, name string) error {
	cli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return err
	}
	defer cli.Close()
	return cli.ContainerRemove(ctx, name, dockercontainer.RemoveOptions{Force: true, RemoveVolumes: true})
}

func cleanupEnv(containerName, runtimePath string) {
	ctx := context.Background()
	// 先终止 daemon 进程，释放 Windows 上的文件锁（daemon.log 等），
	// 否则 t.TempDir 清理时会因文件被占用而失败。
	if info, err := tcdruntime.Read(runtimePath); err == nil && info.PID > 0 {
		killProcess(info.PID)
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if !processAlive(info.PID) {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	_ = cleanupContainerByName(ctx, containerName)
	_ = os.Remove(runtimePath)
	// 清理 runner 目录中的 daemon 副本可执行文件（Windows 特有）
	runnerDir := tcdruntime.RunnerDir(runtimePath)
	_ = os.RemoveAll(runnerDir)
}

func killProcess(pid int) {
	if pid <= 0 {
		return
	}
	if runtime.GOOS == "windows" {
		_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
		return
	}
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Signal(syscall.SIGTERM)
	}
}

// ===========================================================================
// 辅助函数：网络
// ===========================================================================

func pingRedis(addr string) error {
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err = conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return err
	}
	if _, err = conn.Write([]byte("*1\r\n$4\r\nPING\r\n")); err != nil {
		return err
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return err
	}
	if strings.TrimSpace(line) != "+PONG" {
		return fmt.Errorf("unexpected redis response: %q", line)
	}
	return nil
}

// getRedis 向 Redis 发送 GET 命令并验证服务器返回合法的 bulk-string 响应（$-1 或 $N）。
// 用于验证 Redis 连接可用，无论 key 是否存在均视为成功。
func getRedis(addr string) error {
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err = conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return err
	}
	if _, err = conn.Write([]byte("*2\r\n$3\r\nGET\r\n$9\r\ntcd:probe\r\n")); err != nil {
		return err
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return err
	}
	if strings.HasPrefix(strings.TrimSpace(line), "$") {
		return nil
	}
	return fmt.Errorf("unexpected redis GET response: %q", line)
}

func reserveTCPAddr() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	addr := ln.Addr().String()
	if err = ln.Close(); err != nil {
		return "", err
	}
	return addr, nil
}

func probeTCP(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(80 * time.Millisecond)
	}
}

// ===========================================================================
// 辅助函数：进程检查
// ===========================================================================

// blockForever 阻塞当前 goroutine 直到收到终止信号。
// 不使用 select {} 是因为 Go 运行时的死锁检测器会在所有 goroutine 都处于休眠时触发 panic。
func blockForever() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if runtime.GOOS == "windows" {
		out, err := exec.Command("tasklist", "/FI",
			fmt.Sprintf("PID eq %d", pid), "/NH").Output()
		if err != nil {
			return false
		}
		return strings.Contains(string(out), strconv.Itoa(pid))
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}
