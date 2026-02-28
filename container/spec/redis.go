package spec

import (
	"fmt"
	"time"

	tdconstant "github.com/McHarvvvy/testcontainerd/constant"

	"github.com/testcontainers/testcontainers-go/wait"
)

type redisSpec struct{}

func init() {
	Register(redisSpec{})
}

func (redisSpec) Type() string {
	return tdconstant.ContainerTypeRedis
}

func (redisSpec) DefaultPorts() []Port {
	return []Port{{Name: tdconstant.PortNameRedis, ContainerPort: 6379, Protocol: tdconstant.ProtocolTCP}}
}

func (redisSpec) DefaultEnv() map[string]string {
	return map[string]string{}
}

func (redisSpec) WaitStrategy() wait.Strategy {
	return wait.ForAll(
		wait.ForListeningPort(tdconstant.PortRefRedis),
	).WithDeadline(90 * time.Second)
}

func (redisSpec) Command() []string {
	return nil
}

func (redisSpec) BuildMetadata(env map[string]string) map[string]string {
	return map[string]string{}
}

func (redisSpec) BuildURI(host string, ports map[string]int, metadata map[string]string) string {
	return fmt.Sprintf("redis://%s:%d", host, ports[tdconstant.PortNameRedis])
}
