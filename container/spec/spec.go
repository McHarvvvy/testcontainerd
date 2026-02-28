package spec

import (
	"fmt"
	"sync"

	"github.com/testcontainers/testcontainers-go/wait"
)

type Port struct {
	Name          string
	ContainerPort int
	Protocol      string
}

type Driver interface {
	// Type 返回容器类型唯一标识（如 mysql/redis）。
	Type() string
	// DefaultPorts 返回默认端口声明。
	DefaultPorts() []Port
	// DefaultEnv 返回默认环境变量。
	DefaultEnv() map[string]string
	// WaitStrategy 返回容器就绪判定策略。
	WaitStrategy() wait.Strategy
	// Command 返回容器启动命令覆盖。
	Command() []string
	// BuildMetadata 从运行参数提取连接元数据（如用户密码）。
	BuildMetadata(env map[string]string) map[string]string
	// BuildURI 生成统一连接串，便于上层注入到被测服务环境。
	BuildURI(host string, ports map[string]int, metadata map[string]string) string
}

var (
	mu      sync.RWMutex
	drivers = map[string]Driver{}
)

func Register(d Driver) {
	if d == nil {
		panic("container spec driver is nil")
	}
	t := d.Type()
	if t == "" {
		panic("container spec type is empty")
	}

	mu.Lock()
	defer mu.Unlock()
	if _, exists := drivers[t]; exists {
		panic(fmt.Sprintf("container spec duplicated: %s", t))
	}
	drivers[t] = d
}

func Get(containerType string) (Driver, bool) {
	mu.RLock()
	defer mu.RUnlock()
	d, ok := drivers[containerType]
	return d, ok
}
