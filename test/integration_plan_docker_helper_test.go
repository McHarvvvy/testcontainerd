//go:build integration_plan

package test

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	tcd "github.com/McHarvvvy/testcontainerd"
	"github.com/McHarvvvy/testcontainerd/container"
	dockercontainer "github.com/docker/docker/api/types/container"
	dockerclient "github.com/docker/docker/client"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

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
	cmd.Env = append(os.Environ(), "TCD_HELPER_PROCESS=1", "TCD_HELPER_PROBE_ADDR="+d.probeAddr)
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
		select {} // 阻塞保活
	}
	if err := getRedis(redisAddr); err != nil {
		_ = os.WriteFile(envFile, []byte("fail"), 0o644)
	} else {
		_ = os.WriteFile(envFile, []byte("success"), 0o644)
	}
	select {} // 阻塞保活，框架在测试结束后终止此进程
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
	_ = cleanupContainerByName(ctx, containerName)
	_ = os.Remove(runtimePath)
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
