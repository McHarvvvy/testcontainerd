package container

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/McHarvvvy/testcontainerd/protocol"

	"github.com/testcontainers/testcontainers-go"
)

const maxCreateConcurrency = 4

type startResult struct {
	name     string
	resource RuntimeResource
	ctr      testcontainers.Container
	err      error
}

// Bundle 负责测试容器资源生命周期。
type Bundle struct {
	mu          sync.Mutex
	started     bool
	registry    *Registry
	instances   []InstanceConfig
	runtime     map[string]RuntimeResource
	startOrder  []string
	cleanup     []testcontainers.Container
	containerBy map[string]testcontainers.Container
}

// NewBundle 创建容器资源管理对象。
func NewBundle(registry *Registry) *Bundle {
	if registry == nil {
		registry = NewRegistry()
	}
	return &Bundle{
		registry:    registry,
		instances:   nil,
		runtime:     map[string]RuntimeResource{},
		startOrder:  make([]string, 0, 8),
		cleanup:     make([]testcontainers.Container, 0, 8),
		containerBy: map[string]testcontainers.Container{},
	}
}

// RegisteredContainers 返回当前注册的容器配置快照。
func (b *Bundle) RegisteredContainers() []InstanceConfig {
	return b.registry.Snapshot()
}

// StartAll 启动全部依赖容器并返回连接信息。
func (b *Bundle) StartAll(ctx context.Context) (map[string]protocol.ResourceEndpoint, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.started {
		return b.resourcesLocked(), nil
	}
	if len(b.instances) == 0 {
		b.instances = b.registry.Snapshot()
	}
	if len(b.instances) == 0 {
		return nil, fmt.Errorf("no container instances registered")
	}

	ordered := b.instances

	runtimeByName, containerByName, startErr := b.startAllContainers(ctx, ordered)
	if startErr != nil {
		return nil, startErr
	}
	for _, cfg := range ordered {
		resource, ok := runtimeByName[cfg.Name]
		if !ok {
			_ = terminateCreatedContainers(ctx, ordered, containerByName)
			return nil, fmt.Errorf("runtime resource not found after start: %s", cfg.Name)
		}
		ctr, ok := containerByName[cfg.Name]
		if !ok {
			_ = terminateCreatedContainers(ctx, ordered, containerByName)
			return nil, fmt.Errorf("container instance not found after start: %s", cfg.Name)
		}
		b.runtime[cfg.Name] = resource
		b.startOrder = append(b.startOrder, cfg.Name)
		b.cleanup = append(b.cleanup, ctr)
		b.containerBy[cfg.Name] = ctr
	}

	view := newRuntimeView(b.runtime)
	// 关键决策：容器全部启动后再执行 Init，确保初始化逻辑可访问完整运行时视图。
	for _, cfg := range ordered {
		if cfg.Init == nil {
			continue
		}
		self, ok := b.runtime[cfg.Name]
		if !ok {
			_ = b.rollback(ctx)
			return nil, fmt.Errorf("runtime resource not found for init: %s", cfg.Name)
		}
		if err := cfg.Init(ctx, InitInput{Self: self, Runtime: view}); err != nil {
			// 关键决策：初始化失败同样回滚，保证下次 Acquire 仍从干净环境开始。
			_ = b.rollback(ctx)
			return nil, fmt.Errorf("run init for %s failed: %w", cfg.Name, err)
		}
	}

	b.started = true
	return b.resourcesLocked(), nil
}

// StopAll 销毁全部依赖容器。
func (b *Bundle) StopAll(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.stopAllContainers(ctx); err != nil {
		return err
	}
	b.resetState()
	return nil
}

// Started 返回当前容器资源是否已启动。
func (b *Bundle) Started() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.started
}

// Endpoints 返回当前连接信息快照。
func (b *Bundle) Endpoints() map[string]protocol.ResourceEndpoint {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.resourcesLocked()
}

// Containers 返回当前容器实例列表。
func (b *Bundle) Containers() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	result := make([]string, 0, len(b.runtime))
	for name, res := range b.runtime {
		result = append(result, fmt.Sprintf("%s/%s", res.Type, name))
	}
	sort.Strings(result)
	return result
}

func (b *Bundle) rollback(ctx context.Context) error {
	err := b.stopAllContainers(ctx)
	b.resetState()
	return err
}

func (b *Bundle) startAllContainers(ctx context.Context, ordered []InstanceConfig) (map[string]RuntimeResource, map[string]testcontainers.Container, error) {
	runtimeByName := make(map[string]RuntimeResource, len(ordered))
	containerByName := make(map[string]testcontainers.Container, len(ordered))
	if len(ordered) == 0 {
		return runtimeByName, containerByName, nil
	}

	createCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	jobs := make(chan InstanceConfig, len(ordered))
	for _, cfg := range ordered {
		jobs <- cfg
	}
	close(jobs)

	results := make(chan startResult, len(ordered))
	workerN := maxCreateConcurrency
	if len(ordered) < workerN {
		workerN = len(ordered)
	}

	var wg sync.WaitGroup
	for i := 0; i < workerN; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for cfg := range jobs {
				if createCtx.Err() != nil {
					continue
				}
				resource, ctr, err := b.startContainer(createCtx, cfg)
				if err != nil {
					results <- startResult{name: cfg.Name, err: fmt.Errorf("start container %s failed: %w", cfg.Name, err)}
					cancel()
					continue
				}
				results <- startResult{name: cfg.Name, resource: resource, ctr: ctr}
			}
		}()
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	var firstErr error
	for result := range results {
		if result.err != nil {
			if firstErr == nil {
				firstErr = result.err
			}
			continue
		}
		runtimeByName[result.name] = result.resource
		containerByName[result.name] = result.ctr
	}

	if firstErr != nil {
		if terminateErr := terminateCreatedContainers(ctx, ordered, containerByName); terminateErr != nil {
			return nil, nil, fmt.Errorf("%w; rollback failed: %v", firstErr, terminateErr)
		}
		return nil, nil, firstErr
	}
	return runtimeByName, containerByName, nil
}

func terminateCreatedContainers(ctx context.Context, ordered []InstanceConfig, containers map[string]testcontainers.Container) error {
	var firstErr error
	for i := len(ordered) - 1; i >= 0; i-- {
		ctr, ok := containers[ordered[i].Name]
		if !ok || ctr == nil {
			continue
		}
		if err := ctr.Terminate(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (b *Bundle) stopAllContainers(ctx context.Context) error {
	var firstErr error
	// 关键决策：按逆序停止，尽量贴近依赖拓扑（后启动的通常更依赖前置服务）。
	for i := len(b.cleanup) - 1; i >= 0; i-- {
		ctr := b.cleanup[i]
		if ctr == nil {
			continue
		}
		if err := ctr.Terminate(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (b *Bundle) resetState() {
	b.started = false
	b.runtime = map[string]RuntimeResource{}
	b.startOrder = make([]string, 0, len(b.instances))
	b.cleanup = make([]testcontainers.Container, 0, len(b.instances))
	b.containerBy = map[string]testcontainers.Container{}
}

func (b *Bundle) resourcesLocked() map[string]protocol.ResourceEndpoint {
	result := make(map[string]protocol.ResourceEndpoint, len(b.runtime))
	for name, r := range b.runtime {
		ports := make(map[string]int, len(r.Ports))
		for k, v := range r.Ports {
			ports[k] = v
		}
		meta := make(map[string]string, len(r.Metadata))
		for k, v := range r.Metadata {
			meta[k] = v
		}
		result[name] = protocol.ResourceEndpoint{
			Name:     r.Name,
			Type:     string(r.Type),
			Image:    r.Image,
			Host:     r.Host,
			Ports:    ports,
			URI:      r.URI,
			Metadata: meta,
		}
	}
	return result
}
