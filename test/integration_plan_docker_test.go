//go:build integration && integration_plan

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
	"testing"
	"time"

	"github.com/McHarvvvy/testcontainerd/client"
	"github.com/McHarvvvy/testcontainerd/container"
	"github.com/McHarvvvy/testcontainerd/daemon"
	"github.com/McHarvvvy/testcontainerd/tcdruntime"
	"github.com/containerd/errdefs"
	dockerclient "github.com/docker/docker/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTC05AcquireProvidesReadyRedisEndpoint(t *testing.T) {
	requireDocker(t)

	project := fmt.Sprintf("tc05-%d", time.Now().UnixNano())
	runtimePath := filepath.Join(t.TempDir(), "runtime.json")
	redisName := "redis-tc05"

	registry := container.NewRegistry()
	err := registry.Register(container.MustNewInstance(
		redisName,
		container.WithType(container.TypeRedis),
		container.WithImage("redis:7.2-alpine"),
		container.WithPort("redis", 6379, 0),
	))
	require.NoError(t, err)

	stopDaemon := startTestDaemon(t, daemon.Config{
		Addr:        "127.0.0.1:0",
		Project:     project,
		RuntimePath: runtimePath,
		IdleTTL:     5 * time.Second,
	}, registry)
	defer stopDaemon()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	c, err := client.New(ctx, client.Config{
		Project:     project,
		RuntimePath: runtimePath,
		HTTPTimeout: 15 * time.Second,
	})
	require.NoError(t, err)

	lease, err := c.Acquire(ctx)
	require.NoError(t, err)
	defer func() {
		_ = c.Release(context.Background(), lease.LeaseID, 0)
	}()

	redisEP, ok := lease.Resources[redisName]
	require.True(t, ok, "resource %q not found", redisName)
	redisPort, ok := redisEP.Ports["redis"]
	require.True(t, ok, "resource %q redis port not found", redisName)

	addr := fmt.Sprintf("%s:%d", redisEP.Host, redisPort)
	require.NoError(t, pingRedis(addr))
}

func TestTC06SequentialAcquireReusesInfraContainer(t *testing.T) {
	requireDocker(t)

	project := fmt.Sprintf("tc06-%d", time.Now().UnixNano())
	runtimePath := filepath.Join(t.TempDir(), "runtime.json")
	redisName := "redis-tc06"

	registry := container.NewRegistry()
	err := registry.Register(container.MustNewInstance(
		redisName,
		container.WithType(container.TypeRedis),
		container.WithImage("redis:7.2-alpine"),
		container.WithPort("redis", 6379, 0),
	))
	require.NoError(t, err)

	stopDaemon := startTestDaemon(t, daemon.Config{
		Addr:        "127.0.0.1:0",
		Project:     project,
		RuntimePath: runtimePath,
		IdleTTL:     5 * time.Second,
	}, registry)
	defer stopDaemon()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	c1, err := client.New(ctx, client.Config{Project: project, RuntimePath: runtimePath, HTTPTimeout: 15 * time.Second})
	require.NoError(t, err)
	lease1, err := c1.Acquire(ctx)
	require.NoError(t, err)
	_ = c1.Release(context.Background(), lease1.LeaseID, 0)

	c2, err := client.New(ctx, client.Config{Project: project, RuntimePath: runtimePath, HTTPTimeout: 15 * time.Second})
	require.NoError(t, err)
	lease2, err := c2.Acquire(ctx)
	require.NoError(t, err)
	defer func() {
		_ = c2.Release(context.Background(), lease2.LeaseID, 0)
	}()

	ep1 := lease1.Resources[redisName]
	ep2 := lease2.Resources[redisName]
	assert.Equal(t, ep1.Host, ep2.Host)
	assert.Equal(t, ep1.Ports["redis"], ep2.Ports["redis"])
}

func TestTC08RollbackWhenPartOfContainersFailToStart(t *testing.T) {
	requireDocker(t)

	project := fmt.Sprintf("tc08-%d", time.Now().UnixNano())
	runtimePath := filepath.Join(t.TempDir(), "runtime.json")
	goodName := "redis-good-tc08"
	badName := "redis-bad-tc08"

	registry := container.NewRegistry()
	err := registry.Register(container.MustNewInstance(
		goodName,
		container.WithType(container.TypeRedis),
		container.WithImage("redis:7.2-alpine"),
		container.WithPort("redis", 6379, 0),
	))
	require.NoError(t, err)
	err = registry.Register(container.MustNewInstance(
		badName,
		container.WithType(container.TypeRedis),
		container.WithImage("redis:non-existent-tag-for-testcontainerd"),
		container.WithPort("redis", 6379, 0),
	))
	require.NoError(t, err)

	stopDaemon := startTestDaemon(t, daemon.Config{
		Addr:        "127.0.0.1:0",
		Project:     project,
		RuntimePath: runtimePath,
		IdleTTL:     5 * time.Second,
	}, registry)
	defer stopDaemon()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	c, err := client.New(ctx, client.Config{Project: project, RuntimePath: runtimePath, HTTPTimeout: 15 * time.Second})
	require.NoError(t, err)

	_, err = c.Acquire(ctx)
	assert.Error(t, err)

	deadline := time.Now().Add(8 * time.Second)
	for {
		exists, existsErr := containerExists(ctx, goodName)
		require.NoError(t, existsErr)
		if !exists {
			break
		}
		if time.Now().After(deadline) {
			require.FailNowf(t, "rollback timeout", "container %q still exists after rollback", goodName)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func TestTC10AcquireWaitsUntilSUTProbeReady(t *testing.T) {
	requireDocker(t)

	project := fmt.Sprintf("tc10-%d", time.Now().UnixNano())
	runtimePath := filepath.Join(t.TempDir(), "runtime.json")
	probeAddr, err := reserveTCPAddr()
	require.NoError(t, err)

	registry := container.NewRegistry()
	err = registry.Register(container.MustNewInstance(
		"redis-tc10",
		container.WithType(container.TypeRedis),
		container.WithImage("redis:7.2-alpine"),
		container.WithPort("redis", 6379, 0),
	))
	require.NoError(t, err)

	stopDaemon := startTestDaemon(t, daemon.Config{
		Addr:        "127.0.0.1:0",
		Project:     project,
		RuntimePath: runtimePath,
		IdleTTL:     5 * time.Second,
		SUT:         delayedProbeSUT{probeAddr: probeAddr},
	}, registry)
	defer stopDaemon()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	c, err := client.New(ctx, client.Config{Project: project, RuntimePath: runtimePath, HTTPTimeout: 15 * time.Second})
	require.NoError(t, err)

	startAt := time.Now()
	lease, err := c.Acquire(ctx)
	require.NoError(t, err)
	defer func() {
		_ = c.Release(context.Background(), lease.LeaseID, 0)
	}()

	assert.GreaterOrEqual(t, time.Since(startAt), 300*time.Millisecond)
}

type delayedProbeSUT struct {
	probeAddr string
}

func (d delayedProbeSUT) IsEnable() bool { return true }

func (d delayedProbeSUT) GetIdleTTL() time.Duration { return 2 * time.Second }

func (d delayedProbeSUT) GetReadyTimeout() time.Duration { return 10 * time.Second }

func (d delayedProbeSUT) GetGracePeriod() time.Duration { return 2 * time.Second }

func (d delayedProbeSUT) GetCommand(ctx context.Context, in daemon.StartSUTInput) (*exec.Cmd, error) {
	_ = ctx
	_ = in
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperDelayedProbeSUT")
	cmd.Env = append(os.Environ(),
		"TCD_HELPER_PROCESS=1",
		"TCD_HELPER_PROBE_ADDR="+d.probeAddr,
	)
	cmd.Dir = os.TempDir()
	return cmd, nil
}

func (d delayedProbeSUT) GetProbeAddrs() []string { return []string{d.probeAddr} }

func (d delayedProbeSUT) SetEnvEndpoint() error { return nil }

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

func startTestDaemon(t *testing.T, cfg daemon.Config, registry *container.Registry) func() {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	d := daemon.New(cfg, registry)
	errCh := make(chan error, 1)
	go func() {
		errCh <- d.Start(ctx)
	}()

	if _, err := tcdruntime.Wait(cfg.RuntimePath, 10*time.Second); err != nil {
		cancel()
		require.FailNowf(t, "runtime wait", "wait runtime file ready: %v", err)
	}

	return func() {
		cancel()
		select {
		case err := <-errCh:
			require.NoError(t, err)
		case <-time.After(5 * time.Second):
			require.FailNow(t, "timeout waiting daemon exit")
		}
	}
}

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
	if errdefs.IsNotFound(err) || strings.Contains(strings.ToLower(err.Error()), "no such container") {
		return false, nil
	}
	return false, err
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

var _ daemon.SUTBootPlan = delayedProbeSUT{}
