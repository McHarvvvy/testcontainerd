package bootstrap

import (
	"context"

	"github.com/McHarvvvy/testcontainerd/container"
)

// RegisterContainers 注册容器声明（示例最小实现）。
func RegisterContainers(ctx context.Context) ([]container.ContainerRegistration, error) {
	_ = ctx
	return []container.ContainerRegistration{
		{
			Name: "redis-main",
			Start: func(context.Context) (container.StartedContainer, error) {
				return container.StartedContainer{}, nil
			},
		},
	}, nil
}
