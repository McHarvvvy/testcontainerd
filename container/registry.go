package container

import (
	"fmt"
	"sync"

	tdconstant "github.com/McHarvvvy/testcontainerd/constant"
)

// Registry 存储 daemon 启动前注册的容器实例配置。
type Registry struct {
	mu      sync.Mutex
	frozen  bool
	entries map[string]InstanceConfig
	order   []string
}

// NewRegistry 创建注册表。
func NewRegistry() *Registry {
	return &Registry{
		entries: make(map[string]InstanceConfig),
		order:   make([]string, 0, 8),
	}
}

// Register 注册单个容器实例配置。
func (r *Registry) Register(cfg InstanceConfig) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen {
		return fmt.Errorf("registry is frozen")
	}
	if cfg.Name == "" {
		return fmt.Errorf("container name is required")
	}
	if cfg.Type == "" {
		return fmt.Errorf("container type is required: %s", cfg.Name)
	}
	if cfg.Image == "" {
		return fmt.Errorf("container image is required: %s", cfg.Name)
	}
	if _, exists := r.entries[cfg.Name]; exists {
		return fmt.Errorf("duplicated container name: %s", cfg.Name)
	}
	// 关键决策：注册阶段提前做 hostPort 冲突检查，
	// 避免容器真正启动后才在 Docker 层失败，导致排障成本变高。
	for _, p := range cfg.Ports {
		if p.HostPort <= 0 {
			continue
		}
		protocol := p.Protocol
		if protocol == "" {
			protocol = tdconstant.ProtocolTCP
		}
		for _, existed := range r.entries {
			for _, existedPort := range existed.Ports {
				existedProtocol := existedPort.Protocol
				if existedProtocol == "" {
					existedProtocol = tdconstant.ProtocolTCP
				}
				if existedPort.HostPort > 0 && existedPort.HostPort == p.HostPort && existedProtocol == protocol {
					return fmt.Errorf("host port conflict: %d/%s (%s and %s)", p.HostPort, protocol, existed.Name, cfg.Name)
				}
			}
		}
	}
	r.entries[cfg.Name] = cfg
	r.order = append(r.order, cfg.Name)
	return nil
}

// Freeze 冻结注册表，防止 daemon 启动后配置被修改。
func (r *Registry) Freeze() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frozen = true
}

// Snapshot 返回注册配置快照。
func (r *Registry) Snapshot() []InstanceConfig {
	r.mu.Lock()
	defer r.mu.Unlock()
	// 关键决策：返回深拷贝快照，防止调用方修改切片/map 污染注册表原始配置。
	result := make([]InstanceConfig, 0, len(r.order))
	for _, name := range r.order {
		result = append(result, cloneInstanceConfig(r.entries[name]))
	}
	return result
}

func cloneInstanceConfig(cfg InstanceConfig) InstanceConfig {
	copyCfg := cfg
	copyCfg.Ports = append([]PortConfig(nil), cfg.Ports...)
	if len(cfg.Env) > 0 {
		copyCfg.Env = make(map[string]string, len(cfg.Env))
		for k, v := range cfg.Env {
			copyCfg.Env[k] = v
		}
	} else {
		copyCfg.Env = map[string]string{}
	}
	return copyCfg
}
