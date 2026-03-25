package container

import (
	"context"

	"github.com/testcontainers/testcontainers-go"
)

// ContainerRegistration 表示单个容器注册项。
type ContainerRegistration struct {
	// Name 是注册项唯一标识，建议与容器 Name 保持一致，用于回滚、清理和日志。
	Name string
	// Start 负责创建并启动容器，返回容器句柄，由框架托管生命周期。
	Start func(ctx context.Context) (testcontainers.Container, error)
	// Init 在所有容器启动后执行（可选），可用于建库、写种子数据等初始化操作。
	Init func(ctx context.Context) error
}
