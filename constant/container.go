package constant

const (
	ProtocolTCP = "tcp"

	ContainerTypeMySQL  = "mysql"
	ContainerTypeRedis  = "redis"
	ContainerTypeMongo  = "mongo"
	ContainerTypePulsar = "pulsar"

	PortNameMySQL   = "mysql"
	PortNameRedis   = "redis"
	PortNameMongo   = "mongo"
	PortNameService = "service"
	PortNameAdmin   = "admin"

	PortRefMySQL  = "3306/tcp"
	PortRefRedis  = "6379/tcp"
	PortRefMongo  = "27017/tcp"
	PortRefPulsar = "6650/tcp"
	PortRefAdmin  = "8080/tcp"

	MetaKeyUser     = "user"
	MetaKeyPassword = "password"

	EnvMySQLUser         = "MYSQL_USER"
	EnvMySQLRootPassword = "MYSQL_ROOT_PASSWORD"
	EnvMySQLRootHost     = "MYSQL_ROOT_HOST"

	EnvMongoRootUsername = "MONGO_INITDB_ROOT_USERNAME"
	EnvMongoRootPassword = "MONGO_INITDB_ROOT_PASSWORD"

	DefaultRootUser     = "root"
	DefaultRootPassword = "pass"

	PulsarCommandBin        = "bin/pulsar"
	PulsarCommandStandalone = "standalone"
)
