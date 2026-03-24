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
	"strings"
	"sync"
	"testing"
	"time"

	tcd "github.com/McHarvvvy/testcontainerd"
	"github.com/McHarvvvy/testcontainerd/container"
	dockercontainer "github.com/docker/docker/api/types/container"
	dockerclient "github.com/docker/docker/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// ---------------------------------------------------------------------------
// TC-08: 成功接管测试并透传退出码
// ---------------------------------------------------------------------------

func TestTC08SuccessfulRunAndExitCodePassthrough(t *testing.T) {
	requireDocker(t)
	cfg := dockerTestConfig(t)
	registerFn := singleRedisRegisterFunc("redis")

	inst, err := tcd.New(cfg, registerFn)
	require.NoError(t, err)
	t.Cleanup(func() { cleanupEnv("redis", cfg.Global.RuntimePath) })

	// 验证退出码 0 透传
	var verified bool
	code := inst.Run(&fakeRunnable{run: func() int {
		addr, err := redisAddressByContainerName(context.Background(), "redis")
		if err != nil {
			return 1
		}
		if err := pingRedis(addr); err != nil {
			return 1
		}
		verified = true
		return 0
	}})
	assert.Equal(t, 0, code)
	assert.True(t, verified)

	// 验证退出码 1 透传（第二次 Run 复用已有 daemon）
	inst2, err := tcd.New(cfg, registerFn)
	require.NoError(t, err)
	code2 := inst2.Run(&fakeRunnable{run: func() int { return 1 }})
	assert.Equal(t, 1, code2)
}

// ---------------------------------------------------------------------------
// TC-09: 连续执行测试框架复用底层基础设施
// ---------------------------------------------------------------------------

func TestTC09SequentialRunReusesSameContainer(t *testing.T) {
	requireDocker(t)
	cfg := dockerTestConfig(t)
	cfg.Daemon.IdleTTL = 15 * time.Second
	registerFn := singleRedisRegisterFunc("redis")
	t.Cleanup(func() { cleanupEnv("redis", cfg.Global.RuntimePath) })

	var id1, id2 string

	inst1, err := tcd.New(cfg, registerFn)
	require.NoError(t, err)
	inst1.Run(&fakeRunnable{run: func() int {
		id1, _ = containerIDByName(context.Background(), "redis")
		return 0
	}})

	inst2, err := tcd.New(cfg, registerFn)
	require.NoError(t, err)
	inst2.Run(&fakeRunnable{run: func() int {
		id2, _ = containerIDByName(context.Background(), "redis")
		return 0
	}})

	require.NotEmpty(t, id1)
	assert.Equal(t, id1, id2)
}

// ---------------------------------------------------------------------------
// TC-10: 容器部分启动失败触发全量回滚
// ---------------------------------------------------------------------------

func TestTC10PartialStartFailureRollsBack(t *testing.T) {
	requireDocker(t)
	cfg := dockerTestConfig(t)
	registerFn := func(ctx context.Context) ([]container.ContainerRegistration, error) {
		return []container.ContainerRegistration{
			{Name: "redis-good", Start: startRealRedis("redis-good")},
			{Name: "redis-bad", Start: startBadImage("redis-bad")},
		}, nil
	}
	t.Cleanup(func() { cleanupEnv("redis-good", cfg.Global.RuntimePath) })

	inst, err := tcd.New(cfg, registerFn)
	require.NoError(t, err)

	var fakeRan bool
	code := inst.Run(&fakeRunnable{run: func() int {
		fakeRan = true
		return 0
	}})

	assert.Equal(t, 1, code)
	assert.False(t, fakeRan)
	assertContainerGone(t, "redis-good", 10*time.Second)
}

// ---------------------------------------------------------------------------
// TC-11: Init 函数失败触发全量回滚
// ---------------------------------------------------------------------------

func TestTC11InitFailureRollsBackAll(t *testing.T) {
	requireDocker(t)
	cfg := dockerTestConfig(t)
	registerFn := func(ctx context.Context) ([]container.ContainerRegistration, error) {
		return []container.ContainerRegistration{
			{Name: "redis-a", Start: startRealRedis("redis-a")},
			{
				Name:  "redis-b",
				Start: startRealRedis("redis-b"),
				Init: func(ctx context.Context) error {
					return fmt.Errorf("init failed: seed data error")
				},
			},
		}, nil
	}
	t.Cleanup(func() {
		cleanupEnv("redis-a", cfg.Global.RuntimePath)
		_ = cleanupContainerByName(context.Background(), "redis-b")
	})

	inst, err := tcd.New(cfg, registerFn)
	require.NoError(t, err)

	code := inst.Run(&fakeRunnable{run: func() int { return 0 }})

	assert.Equal(t, 1, code)
	assertContainerGone(t, "redis-a", 10*time.Second)
	assertContainerGone(t, "redis-b", 10*time.Second)
}

// ---------------------------------------------------------------------------
// TC-12: 多容器 SUTEnv key 跨注册项冲突
// ---------------------------------------------------------------------------

func TestTC12SUTEnvCrossRegistrationConflict(t *testing.T) {
	requireDocker(t)
	cfg := dockerTestConfig(t)
	registerFn := func(ctx context.Context) ([]container.ContainerRegistration, error) {
		return []container.ContainerRegistration{
			{Name: "redis-a", Start: startRedisWithSUTEnv("redis-a", map[string]string{"DB_ADDR": "a:6379"})},
			{Name: "redis-b", Start: startRedisWithSUTEnv("redis-b", map[string]string{"DB_ADDR": "b:6379"})},
		}, nil
	}
	t.Cleanup(func() {
		cleanupEnv("redis-a", cfg.Global.RuntimePath)
		_ = cleanupContainerByName(context.Background(), "redis-b")
	})

	inst, err := tcd.New(cfg, registerFn)
	require.NoError(t, err)

	code := inst.Run(&fakeRunnable{run: func() int { return 0 }})

	assert.Equal(t, 1, code)
	assertContainerGone(t, "redis-a", 10*time.Second)
	assertContainerGone(t, "redis-b", 10*time.Second)
}

// ---------------------------------------------------------------------------
// TC-13: cmd.Env 与容器 SUTEnv key 冲突
// ---------------------------------------------------------------------------

func TestTC13CmdEnvSUTEnvConflict(t *testing.T) {
	requireDocker(t)
	probeAddr, err := reserveTCPAddr()
	require.NoError(t, err)

	cfg := dockerTestConfig(t)
	cfg.SUT = conflictEnvSUT{probeAddr: probeAddr}
	registerFn := func(ctx context.Context) ([]container.ContainerRegistration, error) {
		return []container.ContainerRegistration{
			{Name: "redis", Start: startRedisWithSUTEnv("redis", map[string]string{"APP_PORT": "3306"})},
		}, nil
	}
	t.Cleanup(func() { cleanupEnv("redis", cfg.Global.RuntimePath) })

	inst, err := tcd.New(cfg, registerFn)
	require.NoError(t, err)

	code := inst.Run(&fakeRunnable{run: func() int { return 0 }})

	assert.Equal(t, 1, code)
}

// ---------------------------------------------------------------------------
// TC-14: SUTEnv 成功注入 SUT 进程环境变量
// ---------------------------------------------------------------------------

func TestTC14SUTEnvInjectedIntoSUTProcess(t *testing.T) {
	requireDocker(t)
	envFile := filepath.Join(t.TempDir(), "sut_env_output.txt")
	probeAddr, err := reserveTCPAddr()
	require.NoError(t, err)

	cfg := dockerTestConfig(t)
	cfg.SUT = sutEnvVerifySUT{envFile: envFile, probeAddr: probeAddr}
	registerFn := func(ctx context.Context) ([]container.ContainerRegistration, error) {
		return []container.ContainerRegistration{
			{Name: "redis", Start: func(ctx context.Context) (container.StartedContainer, error) {
				ctr, ctrErr := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
					ContainerRequest: testcontainers.ContainerRequest{
						Name:         "redis",
						Image:        "redis:7.2-alpine",
						ExposedPorts: []string{"6379/tcp"},
						WaitingFor:   wait.ForListeningPort("6379/tcp"),
					},
					Started: true,
				})
				if ctrErr != nil {
					return container.StartedContainer{}, ctrErr
				}
				host, _ := ctr.Host(ctx)
				port, _ := ctr.MappedPort(ctx, "6379")
				addr := net.JoinHostPort(host, port.Port())
				return container.StartedContainer{
					Container: ctr,
					SUTEnv:    map[string]string{"TEST_REDIS_ADDR": addr},
				}, nil
			}},
		}, nil
	}
	t.Cleanup(func() { cleanupEnv("redis", cfg.Global.RuntimePath) })

	inst, err := tcd.New(cfg, registerFn)
	require.NoError(t, err)

	var verified bool
	code := inst.Run(&fakeRunnable{run: func() int {
		data, readErr := os.ReadFile(envFile)
		if readErr != nil {
			return 1
		}
		actualAddr, inspectErr := redisAddressByContainerName(context.Background(), "redis")
		if inspectErr != nil {
			return 1
		}
		if strings.TrimSpace(string(data)) != actualAddr {
			return 1
		}
		verified = true
		return 0
	}})
	assert.Equal(t, 0, code)
	assert.True(t, verified)
}

// ---------------------------------------------------------------------------
// TC-15: SUT 就绪探测保证 fakeM.Run() 执行时端口已就绪
// ---------------------------------------------------------------------------

func TestTC15SUTProbeReadyBeforeFakeMRun(t *testing.T) {
	requireDocker(t)
	probeAddr, err := reserveTCPAddr()
	require.NoError(t, err)

	cfg := dockerTestConfig(t)
	cfg.SUT = delayedProbeSUT{probeAddr: probeAddr}
	registerFn := singleRedisRegisterFunc("redis")
	t.Cleanup(func() { cleanupEnv("redis", cfg.Global.RuntimePath) })

	inst, err := tcd.New(cfg, registerFn)
	require.NoError(t, err)

	var probeOK bool
	code := inst.Run(&fakeRunnable{run: func() int {
		if probeErr := probeTCP(probeAddr, 2*time.Second); probeErr != nil {
			return 1
		}
		probeOK = true
		return 0
	}})
	assert.Equal(t, 0, code)
	assert.True(t, probeOK)
}

// ---------------------------------------------------------------------------
// TC-16: Daemon 空闲退出
// ---------------------------------------------------------------------------

func TestTC16DaemonIdleExitCleansUp(t *testing.T) {
	requireDocker(t)
	cfg := dockerTestConfig(t)
	cfg.Daemon.IdleTTL = 1 * time.Second
	registerFn := singleRedisRegisterFunc("redis")
	runtimePath := cfg.Global.RuntimePath
	t.Cleanup(func() { cleanupEnv("redis", runtimePath) })

	inst, err := tcd.New(cfg, registerFn)
	require.NoError(t, err)

	code := inst.Run(&fakeRunnable{run: func() int { return 0 }})
	assert.Equal(t, 0, code)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_, statErr := os.Stat(runtimePath)
		runtimeGone := os.IsNotExist(statErr)
		exists, existsErr := containerExists(context.Background(), "redis")
		if existsErr == nil && runtimeGone && !exists {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatal("daemon idle exit timeout: runtime or container still exists")
}

// ---------------------------------------------------------------------------
// TC-17: 多 client 并发 Acquire 复用同一容器
// ---------------------------------------------------------------------------

func TestTC17ConcurrentRunReusesSameContainer(t *testing.T) {
	requireDocker(t)
	cfg := dockerTestConfig(t)
	cfg.Daemon.IdleTTL = 15 * time.Second
	registerFn := singleRedisRegisterFunc("redis")
	t.Cleanup(func() { cleanupEnv("redis", cfg.Global.RuntimePath) })

	containerIDs := make(chan string, 3)
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			inst, instErr := tcd.New(cfg, registerFn)
			if instErr != nil {
				return
			}
			inst.Run(&fakeRunnable{run: func() int {
				id, _ := containerIDByName(context.Background(), "redis")
				containerIDs <- id
				return 0
			}})
		}()
	}
	wg.Wait()
	close(containerIDs)

	ids := make(map[string]struct{})
	for id := range containerIDs {
		ids[id] = struct{}{}
	}
	assert.Len(t, ids, 1)
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
	cmd.Env = append(os.Environ(), "TCD_HELPER_PROCESS=1", "TCD_HELPER_PROBE_ADDR="+d.probeAddr)
	cmd.Dir = os.TempDir()
	return cmd, nil
}

var _ tcd.SUTBootPlan = delayedProbeSUT{}

// conflictEnvSUT — TC-13: GetCommand 返回 cmd.Env 中含 "APP_PORT=8080"，与容器 SUTEnv 冲突。
type conflictEnvSUT struct {
	probeAddr string
}

func (c conflictEnvSUT) IsEnable() bool                 { return true }
func (c conflictEnvSUT) GetIdleTTL() time.Duration      { return 2 * time.Second }
func (c conflictEnvSUT) GetReadyTimeout() time.Duration { return 10 * time.Second }
func (c conflictEnvSUT) GetGracePeriod() time.Duration  { return 2 * time.Second }
func (c conflictEnvSUT) GetProbeAddrs() []string        { return []string{c.probeAddr} }
func (c conflictEnvSUT) GetCommand(_ context.Context, _ tcd.StartSUTInput) (*exec.Cmd, error) {
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperDelayedProbeSUT")
	cmd.Env = append(os.Environ(), "TCD_HELPER_PROCESS=1", "TCD_HELPER_PROBE_ADDR="+c.probeAddr, "APP_PORT=8080")
	cmd.Dir = os.TempDir()
	return cmd, nil
}

var _ tcd.SUTBootPlan = conflictEnvSUT{}

// sutEnvVerifySUT — TC-14: SUT helper 读取 TEST_REDIS_ADDR 写入文件后监听探测端口。
type sutEnvVerifySUT struct {
	envFile   string
	probeAddr string
}

func (s sutEnvVerifySUT) IsEnable() bool                 { return true }
func (s sutEnvVerifySUT) GetIdleTTL() time.Duration      { return 2 * time.Second }
func (s sutEnvVerifySUT) GetReadyTimeout() time.Duration { return 10 * time.Second }
func (s sutEnvVerifySUT) GetGracePeriod() time.Duration  { return 2 * time.Second }
func (s sutEnvVerifySUT) GetProbeAddrs() []string        { return []string{s.probeAddr} }
func (s sutEnvVerifySUT) GetCommand(_ context.Context, _ tcd.StartSUTInput) (*exec.Cmd, error) {
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperSUTEnvVerify")
	cmd.Env = append(os.Environ(),
		"TCD_HELPER_PROCESS=1",
		"TCD_HELPER_ENV_FILE="+s.envFile,
		"TCD_HELPER_PROBE_ADDR="+s.probeAddr,
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

// TestHelperSUTEnvVerify — TC-14 的 SUT helper 进程：读取 TEST_REDIS_ADDR 写入文件后监听探测端口。
func TestHelperSUTEnvVerify(t *testing.T) {
	if os.Getenv("TCD_HELPER_PROCESS") != "1" {
		return
	}
	envFile := strings.TrimSpace(os.Getenv("TCD_HELPER_ENV_FILE"))
	probeAddr := strings.TrimSpace(os.Getenv("TCD_HELPER_PROBE_ADDR"))
	if envFile == "" || probeAddr == "" {
		os.Exit(2)
	}
	redisAddr := os.Getenv("TEST_REDIS_ADDR")
	if err := os.WriteFile(envFile, []byte(redisAddr), 0o644); err != nil {
		os.Exit(3)
	}
	ln, err := net.Listen("tcp", probeAddr)
	if err != nil {
		os.Exit(4)
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

func startRealRedis(name string) func(ctx context.Context) (container.StartedContainer, error) {
	return func(ctx context.Context) (container.StartedContainer, error) {
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
			return container.StartedContainer{}, err
		}
		return container.StartedContainer{
			Container: ctr,
			SUTEnv:    map[string]string{},
		}, nil
	}
}

func startBadImage(name string) func(ctx context.Context) (container.StartedContainer, error) {
	return func(ctx context.Context) (container.StartedContainer, error) {
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
			return container.StartedContainer{}, err
		}
		return container.StartedContainer{Container: ctr}, nil
	}
}

func startRedisWithSUTEnv(name string, env map[string]string) func(ctx context.Context) (container.StartedContainer, error) {
	return func(ctx context.Context) (container.StartedContainer, error) {
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
			return container.StartedContainer{}, err
		}
		return container.StartedContainer{
			Container: ctr,
			SUTEnv:    env,
		}, nil
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

func assertContainerGone(t *testing.T, name string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		exists, err := containerExists(context.Background(), name)
		if err == nil && !exists {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("container %q still exists after %v", name, timeout)
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
