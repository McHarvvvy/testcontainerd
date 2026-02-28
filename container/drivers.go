package container

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tdconstant "github.com/McHarvvvy/testcontainerd/constant"
	"github.com/McHarvvvy/testcontainerd/container/spec"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/testcontainers/testcontainers-go"
)

func (b *Bundle) startContainer(ctx context.Context, cfg InstanceConfig) (RuntimeResource, testcontainers.Container, error) {
	req, namedPorts, d, env, err := buildContainerRequest(cfg)
	if err != nil {
		return RuntimeResource{}, nil, err
	}

	ctr, err := createContainerWithRetry(ctx, req)
	if err != nil {
		return RuntimeResource{}, nil, err
	}

	host, err := ctr.Host(ctx)
	if err != nil {
		_ = ctr.Terminate(ctx)
		return RuntimeResource{}, nil, err
	}

	ports := make(map[string]int, len(namedPorts))
	for name, portRef := range namedPorts {
		mappedPort, portErr := ctr.MappedPort(ctx, nat.Port(portRef))
		if portErr != nil {
			_ = ctr.Terminate(ctx)
			return RuntimeResource{}, nil, portErr
		}
		ports[name] = mappedPort.Int()
	}

	resource := RuntimeResource{
		Name:     cfg.Name,
		Type:     cfg.Type,
		Image:    cfg.Image,
		Host:     host,
		Ports:    ports,
		Metadata: d.BuildMetadata(env),
	}
	resource.URI = d.BuildURI(resource.Host, resource.Ports, resource.Metadata)
	return resource, ctr, nil
}

func createContainerWithRetry(ctx context.Context, req testcontainers.ContainerRequest) (testcontainers.Container, error) {
	var err error
	for attempt := 0; attempt < 4; attempt++ {
		var ctr testcontainers.Container
		ctr, err = testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: req,
			Started:          true,
		})
		if err == nil {
			return ctr, nil
		}
		// 关键决策：这里只对“可恢复的短暂冲突”重试，避免掩盖真实配置错误。
		if !shouldRetryCreate(ctx, req.Name, err) || attempt == 3 {
			return nil, err
		}
		time.Sleep(time.Duration(attempt+1) * 300 * time.Millisecond)
	}
	return nil, err
}

func shouldRetryCreate(ctx context.Context, containerName string, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if cerrdefs.IsConflict(err) {
		return true
	}
	if containerName == "" {
		return false
	}

	removing, inspectErr := isContainerRemoving(ctx, containerName)
	if inspectErr != nil {
		return false
	}
	return removing
}

func isContainerRemoving(ctx context.Context, containerName string) (bool, error) {
	// 关键决策：通过 Docker Inspect 判定 removing 状态，避免依赖错误字符串匹配。
	dockerCli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return false, err
	}
	defer dockerCli.Close()

	inspect, err := dockerCli.ContainerInspect(ctx, containerName)
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	if inspect.State == nil {
		return false, nil
	}
	return strings.EqualFold(inspect.State.Status, "removing"), nil
}

func buildContainerRequest(cfg InstanceConfig) (testcontainers.ContainerRequest, map[string]string, spec.Driver, map[string]string, error) {
	if cfg.Type == "" {
		return testcontainers.ContainerRequest{}, nil, nil, nil, fmt.Errorf("container type is required: %s", cfg.Name)
	}
	d, ok := spec.Get(string(cfg.Type))
	if !ok {
		return testcontainers.ContainerRequest{}, nil, nil, nil, fmt.Errorf("unsupported container type: %s", cfg.Type)
	}

	namedPorts := make(map[string]string, len(cfg.Ports))
	exposed := make([]string, 0, len(cfg.Ports))
	for _, p := range cfg.Ports {
		proto := strings.ToLower(strings.TrimSpace(p.Protocol))
		if proto == "" {
			proto = tdconstant.ProtocolTCP
		}
		if p.ContainerPort <= 0 {
			return testcontainers.ContainerRequest{}, nil, nil, nil, fmt.Errorf("invalid container port for %s", cfg.Name)
		}
		if p.Name == "" {
			return testcontainers.ContainerRequest{}, nil, nil, nil, fmt.Errorf("port name is required for %s", cfg.Name)
		}
		portRef := fmt.Sprintf("%d/%s", p.ContainerPort, proto)
		namedPorts[p.Name] = portRef
		if p.HostPort > 0 {
			exposed = append(exposed, fmt.Sprintf("%d:%d/%s", p.HostPort, p.ContainerPort, proto))
			continue
		}
		exposed = append(exposed, portRef)
	}

	env := d.DefaultEnv()
	for k, v := range cfg.Env {
		env[k] = v
	}
	return testcontainers.ContainerRequest{
		// 关键决策：容器名直接使用实例配置名，便于排障与资源识别；
		// 同时配合 daemon 启动前清理同名残留容器，避免名称冲突导致启动失败。
		Name:         cfg.Name,
		Image:        cfg.Image,
		ExposedPorts: exposed,
		Env:          env,
		Cmd:          d.Command(),
		WaitingFor:   d.WaitStrategy(),
	}, namedPorts, d, env, nil
}
