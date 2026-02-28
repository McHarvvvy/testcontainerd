package spec

import (
	"fmt"
	"time"

	tdconstant "github.com/McHarvvvy/testcontainerd/constant"
	"github.com/testcontainers/testcontainers-go/wait"
)

type pulsarSpec struct{}

func init() {
	Register(pulsarSpec{})
}

func (pulsarSpec) Type() string {
	return tdconstant.ContainerTypePulsar
}

func (pulsarSpec) DefaultPorts() []Port {
	return []Port{
		{Name: tdconstant.PortNameService, ContainerPort: 6650, Protocol: tdconstant.ProtocolTCP},
		{Name: tdconstant.PortNameAdmin, ContainerPort: 8080, Protocol: tdconstant.ProtocolTCP},
	}
}

func (pulsarSpec) DefaultEnv() map[string]string {
	return map[string]string{}
}

func (pulsarSpec) WaitStrategy() wait.Strategy {
	// 关键决策：仅以端口可达作为就绪条件。
	// Pulsar 不同版本镜像日志文案差异较大，依赖固定日志关键字会导致误判超时。
	return wait.ForAll(
		wait.ForListeningPort(tdconstant.PortRefPulsar),
		wait.ForListeningPort(tdconstant.PortRefAdmin),
	).WithDeadline(180 * time.Second)
}

func (pulsarSpec) Command() []string {
	return []string{tdconstant.PulsarCommandBin, tdconstant.PulsarCommandStandalone}
}

func (pulsarSpec) BuildMetadata(env map[string]string) map[string]string {
	return map[string]string{}
}

func (pulsarSpec) BuildURI(host string, ports map[string]int, metadata map[string]string) string {
	return fmt.Sprintf("pulsar://%s:%d", host, ports[tdconstant.PortNameService])
}
