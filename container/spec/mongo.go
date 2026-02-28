package spec

import (
	"fmt"
	"time"

	tdconstant "github.com/McHarvvvy/testcontainerd/constant"

	"github.com/testcontainers/testcontainers-go/wait"
)

type mongoSpec struct{}

func init() {
	Register(mongoSpec{})
}

func (mongoSpec) Type() string {
	return tdconstant.ContainerTypeMongo
}

func (mongoSpec) DefaultPorts() []Port {
	return []Port{{Name: tdconstant.PortNameMongo, ContainerPort: 27017, Protocol: tdconstant.ProtocolTCP}}
}

func (mongoSpec) DefaultEnv() map[string]string {
	return map[string]string{
		tdconstant.EnvMongoRootUsername: tdconstant.DefaultRootUser,
		tdconstant.EnvMongoRootPassword: tdconstant.DefaultRootPassword,
	}
}

func (mongoSpec) WaitStrategy() wait.Strategy {
	return wait.ForAll(
		wait.ForListeningPort(tdconstant.PortRefMongo),
	).WithDeadline(120 * time.Second)
}

func (mongoSpec) Command() []string {
	return nil
}

func (mongoSpec) BuildMetadata(env map[string]string) map[string]string {
	user := env[tdconstant.EnvMongoRootUsername]
	if user == "" {
		user = tdconstant.DefaultRootUser
	}
	password := env[tdconstant.EnvMongoRootPassword]
	if password == "" {
		password = tdconstant.DefaultRootPassword
	}
	return map[string]string{
		tdconstant.MetaKeyUser:     user,
		tdconstant.MetaKeyPassword: password,
	}
}

func (mongoSpec) BuildURI(host string, ports map[string]int, metadata map[string]string) string {
	user := metadata[tdconstant.MetaKeyUser]
	if user == "" {
		user = tdconstant.DefaultRootUser
	}
	password := metadata[tdconstant.MetaKeyPassword]
	if password == "" {
		password = tdconstant.DefaultRootPassword
	}
	return fmt.Sprintf("mongodb://%s:%s@%s:%d/admin?authSource=admin", user, password, host, ports[tdconstant.PortNameMongo])
}
