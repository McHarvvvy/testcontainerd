package spec

import (
	"fmt"
	"time"

	tdconstant "github.com/McHarvvvy/testcontainerd/constant"

	"github.com/testcontainers/testcontainers-go/wait"
)

type mysqlSpec struct{}

func init() {
	Register(mysqlSpec{})
}

func (mysqlSpec) Type() string {
	return tdconstant.ContainerTypeMySQL
}

func (mysqlSpec) DefaultPorts() []Port {
	return []Port{{Name: tdconstant.PortNameMySQL, ContainerPort: 3306, Protocol: tdconstant.ProtocolTCP}}
}

func (mysqlSpec) DefaultEnv() map[string]string {
	return map[string]string{
		tdconstant.EnvMySQLRootPassword: tdconstant.DefaultRootPassword,
		tdconstant.EnvMySQLRootHost:     "%",
	}
}

func (mysqlSpec) WaitStrategy() wait.Strategy {
	return wait.ForAll(
		wait.ForListeningPort(tdconstant.PortRefMySQL),
	).WithDeadline(120 * time.Second)
}

func (mysqlSpec) Command() []string {
	return nil
}

func (mysqlSpec) BuildMetadata(env map[string]string) map[string]string {
	user := env[tdconstant.EnvMySQLUser]
	if user == "" {
		user = tdconstant.DefaultRootUser
	}
	password := env[tdconstant.EnvMySQLRootPassword]
	if password == "" {
		password = tdconstant.DefaultRootPassword
	}
	return map[string]string{
		tdconstant.MetaKeyUser:     user,
		tdconstant.MetaKeyPassword: password,
	}
}

func (mysqlSpec) BuildURI(host string, ports map[string]int, metadata map[string]string) string {
	user := metadata[tdconstant.MetaKeyUser]
	if user == "" {
		user = tdconstant.DefaultRootUser
	}
	password := metadata[tdconstant.MetaKeyPassword]
	if password == "" {
		password = tdconstant.DefaultRootPassword
	}
	return fmt.Sprintf("%s:%s@%s(%s:%d)/?charset=utf8&parseTime=True&loc=Local&multiStatements=true", user, password, tdconstant.ProtocolTCP, host, ports[tdconstant.PortNameMySQL])
}
