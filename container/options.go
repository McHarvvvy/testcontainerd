package container

import (
	"context"
	"fmt"

	tdconstant "github.com/McHarvvvy/testcontainerd/constant"
	"github.com/McHarvvvy/testcontainerd/container/spec"
	"github.com/testcontainers/testcontainers-go"
)

// StartedContainer 是调用方 Start 函数的返回值。
// Container 由框架托管生命周期；SUTEnv 用于向 SUT 注入连接类环境变量。
type StartedContainer struct {
	Container testcontainers.Container
	SUTEnv    map[string]string
}

// ContainerRegistration 表示单个容器注册项。
type ContainerRegistration struct {
	Name  string
	Start func(ctx context.Context) (StartedContainer, error)
	Init  func(ctx context.Context) error
}

// ContainerType 表示容器类型。
type ContainerType string

const (
	TypeMySQL  ContainerType = ContainerType(tdconstant.ContainerTypeMySQL)
	TypeRedis  ContainerType = ContainerType(tdconstant.ContainerTypeRedis)
	TypeMongo  ContainerType = ContainerType(tdconstant.ContainerTypeMongo)
	TypePulsar ContainerType = ContainerType(tdconstant.ContainerTypePulsar)
)

// PortConfig 描述容器端口映射。
type PortConfig struct {
	Name          string
	ContainerPort int
	Protocol      string
	HostPort      int
}

// InitInput 表示初始化函数输入。
type InitInput struct {
	Self    RuntimeResource
	Runtime RuntimeView
}

// InitFunc 表示容器初始化函数。
type InitFunc func(ctx context.Context, in InitInput) error

// InstanceConfig 表示单个容器实例配置。
type InstanceConfig struct {
	Name  string
	Type  ContainerType
	Image string
	Ports []PortConfig
	Env   map[string]string
	Init  InitFunc
}

// Option 表示实例配置选项。
type Option func(*InstanceConfig) error

// NewInstance 创建容器实例配置。
func NewInstance(name string, opts ...Option) (InstanceConfig, error) {
	cfg := InstanceConfig{
		Name:  name,
		Ports: []PortConfig{},
		Env:   map[string]string{},
	}
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(&cfg); err != nil {
			return InstanceConfig{}, err
		}
	}
	if cfg.Name == "" {
		return InstanceConfig{}, fmt.Errorf("container name is required")
	}
	if cfg.Type == "" {
		return InstanceConfig{}, fmt.Errorf("container type is required: %s", cfg.Name)
	}
	if cfg.Image == "" {
		return InstanceConfig{}, fmt.Errorf("container image is required: %s", cfg.Name)
	}
	if len(cfg.Ports) == 0 {
		d, ok := spec.Get(string(cfg.Type))
		if !ok {
			return InstanceConfig{}, fmt.Errorf("unsupported container type: %s", cfg.Type)
		}
		cfg.Ports = make([]PortConfig, 0, len(d.DefaultPorts()))
		for _, p := range d.DefaultPorts() {
			protocol := p.Protocol
			if protocol == "" {
				protocol = tdconstant.ProtocolTCP
			}
			cfg.Ports = append(cfg.Ports, PortConfig{
				Name:          p.Name,
				ContainerPort: p.ContainerPort,
				Protocol:      protocol,
			})
		}
	}
	return cfg, nil
}

// MustNewInstance 创建实例配置，失败时 panic。
func MustNewInstance(name string, opts ...Option) InstanceConfig {
	cfg, err := NewInstance(name, opts...)
	if err != nil {
		panic(err)
	}
	return cfg
}

// WithType 设置容器类型。
func WithType(t ContainerType) Option {
	return func(cfg *InstanceConfig) error {
		if t == "" {
			return fmt.Errorf("container type cannot be empty")
		}
		cfg.Type = t
		return nil
	}
}

// WithImage 设置容器镜像。
func WithImage(image string) Option {
	return func(cfg *InstanceConfig) error {
		if image == "" {
			return fmt.Errorf("image cannot be empty")
		}
		cfg.Image = image
		return nil
	}
}

// WithPort 增加端口映射配置。
func WithPort(name string, containerPort int, hostPort int) Option {
	return func(cfg *InstanceConfig) error {
		if name == "" {
			return fmt.Errorf("port name cannot be empty")
		}
		if containerPort <= 0 {
			return fmt.Errorf("container port must be positive")
		}
		cfg.Ports = append(cfg.Ports, PortConfig{
			Name:          name,
			ContainerPort: containerPort,
			Protocol:      tdconstant.ProtocolTCP,
			HostPort:      hostPort,
		})
		return nil
	}
}

// WithPortProtocol 增加端口映射并指定协议。
func WithPortProtocol(name string, containerPort int, protocol string, hostPort int) Option {
	return func(cfg *InstanceConfig) error {
		if name == "" {
			return fmt.Errorf("port name cannot be empty")
		}
		if containerPort <= 0 {
			return fmt.Errorf("container port must be positive")
		}
		if protocol == "" {
			protocol = tdconstant.ProtocolTCP
		}
		cfg.Ports = append(cfg.Ports, PortConfig{
			Name:          name,
			ContainerPort: containerPort,
			Protocol:      protocol,
			HostPort:      hostPort,
		})
		return nil
	}
}

// WithEnv 设置容器环境变量。
func WithEnv(key, value string) Option {
	return func(cfg *InstanceConfig) error {
		if key == "" {
			return fmt.Errorf("env key cannot be empty")
		}
		cfg.Env[key] = value
		return nil
	}
}

// WithInit 设置容器初始化函数。
func WithInit(fn InitFunc) Option {
	return func(cfg *InstanceConfig) error {
		cfg.Init = fn
		return nil
	}
}
