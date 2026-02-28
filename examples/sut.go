package bootstrap

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/McHarvvvy/testcontainerd"
	"github.com/McHarvvvy/testcontainerd/protocol"
)

var sutNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

type sutBootPlan struct {
	Enabled         bool
	IdleTTL         time.Duration
	StopGracePeriod time.Duration
	HTTPAddrs       []string
	GRPCAddr        []string
	ReadyTimeout    time.Duration
}

func newSUTBootPlan() testcontainerd.SUTBootPlan {
	return &sutBootPlan{
		Enabled:         true,
		IdleTTL:         2 * time.Second,
		StopGracePeriod: 5 * time.Second,
		HTTPAddrs:       []string{"127.0.0.1:10000"},
		GRPCAddr:        []string{"127.0.0.1:10001"},
		ReadyTimeout:    45 * time.Second,
	}
}

func (p *sutBootPlan) IsEnable() bool {
	return p.Enabled
}

func (p *sutBootPlan) GetIdleTTL() time.Duration {
	return p.IdleTTL
}

func (p *sutBootPlan) GetReadyTimeout() time.Duration {
	return p.ReadyTimeout
}

func (p *sutBootPlan) GetGracePeriod() time.Duration {
	return p.StopGracePeriod
}

func (p *sutBootPlan) GetCommand(ctx context.Context, in testcontainerd.StartSUTInput) (*exec.Cmd, error) {
	env, err := buildSUTEnv(in.Resources)
	if err != nil {
		return nil, err
	}
	sutName, err := validateSUTName(in.Project)
	if err != nil {
		return nil, err
	}
	mainDir := "E:\\Coding\\Workspace\\Golang\\src\\mssiot_user\\cmd"
	binaryName := sutName
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}
	binaryPath := filepath.Join(mainDir, binaryName)
	buildCmd := exec.CommandContext(ctx, "go", "build", "-o", binaryPath, ".")
	buildCmd.Dir = mainDir
	if output, buildErr := buildCmd.CombinedOutput(); buildErr != nil {
		return nil, fmt.Errorf("build sut failed: %w, output: %s", buildErr, strings.TrimSpace(string(output)))
	}
	// 关键决策：SUT 进程生命周期由 daemon 管控，不能绑定到单次 Acquire 请求上下文，
	// 否则请求结束后 context 取消会导致 SUT 被提前杀掉并触发重复拉起。
	cmd := exec.CommandContext(context.Background(), binaryPath)
	cmd.Dir = mainDir
	cmd.Env = mergeEnv(os.Environ(), env)
	return cmd, nil
}

func validateSUTName(project string) (string, error) {
	name := strings.TrimSpace(project)
	if name == "" {
		return "", fmt.Errorf("project name is required")
	}
	if name == "." || name == ".." {
		return "", fmt.Errorf("project name is invalid: %s", project)
	}
	if !sutNamePattern.MatchString(name) {
		return "", fmt.Errorf("project name is invalid for filename: %s", project)
	}
	return name, nil
}

func (p *sutBootPlan) GetProbeAddrs() []string {
	m := make([]string, 0, len(p.HTTPAddrs)+len(p.GRPCAddr))
	m = append(m, p.HTTPAddrs...)
	m = append(m, p.GRPCAddr...)
	return m
}

func (p *sutBootPlan) SetEnvEndpoint() error {
	if err := os.Setenv("MSSIOT_IT_APP_HTTP_ADDR", p.HTTPAddrs[0]); err != nil {
		return err
	}
	if err := os.Setenv("MSSIOT_IT_APP_GRPC_ADDR", p.GRPCAddr[0]); err != nil {
		return err
	}
	return nil
}

func buildSUTEnv(resources map[string]protocol.ResourceEndpoint) (map[string]string, error) {
	mysql, ok := resources["mysql-main"]
	if !ok {
		return nil, fmt.Errorf("resource mysql-main not found")
	}
	redis, ok := resources["redis-main"]
	if !ok {
		return nil, fmt.Errorf("resource redis-main not found")
	}
	mongo, ok := resources["mongo-main"]
	if !ok {
		return nil, fmt.Errorf("resource mongo-main not found")
	}
	pulsar, ok := resources["pulsar-main"]
	if !ok {
		return nil, fmt.Errorf("resource pulsar-main not found")
	}

	user := mysql.Metadata["user"]
	if user == "" {
		user = "root"
	}
	password := mysql.Metadata["password"]
	if password == "" {
		password = "pass"
	}
	mysqlPort, ok := mysql.Ports["mysql"]
	if !ok {
		return nil, fmt.Errorf("mysql-main.mysql port not found")
	}
	redisPort, ok := redis.Ports["redis"]
	if !ok {
		return nil, fmt.Errorf("redis-main.redis port not found")
	}

	env := map[string]string{
		"MSSIOT_IT_ENABLED":                 "true",
		"MSSIOT_ENV":                        "local",
		"MSSIOT_REGION":                     "hhw",
		"MSSIOT_IT_MYSQL_USER_DSN":          mysqlDSN(user, password, mysql.Host, mysqlPort, "meross_user"),
		"MSSIOT_IT_MYSQL_SMART_DSN":         mysqlDSN(user, password, mysql.Host, mysqlPort, "meross_smart"),
		"MSSIOT_IT_MYSQL_COMMON_CONFIG_DSN": mysqlDSN(user, password, mysql.Host, mysqlPort, "global_common_config"),
		"MSSIOT_IT_MYSQL_COMMON_DATA_DSN":   mysqlDSN(user, password, mysql.Host, mysqlPort, "global_common_data"),
		"MSSIOT_IT_MYSQL_SHARE_DSN":         mysqlDSN(user, password, mysql.Host, mysqlPort, "global_share_resource"),
		"MSSIOT_IT_MYSQL_JOURNAL_DSN":       mysqlDSN(user, password, mysql.Host, mysqlPort, "meross_journal"),
		"MSSIOT_IT_REDIS_ADDR":              fmt.Sprintf("%s:%d", redis.Host, redisPort),
		"MSSIOT_IT_PUSH_REDIS_ADDR":         fmt.Sprintf("%s:%d", redis.Host, redisPort),
		"MSSIOT_IT_MONGO_URI":               mongo.URI,
		"MSSIOT_IT_PULSAR_URL":              pulsar.URI,
	}
	return env, nil
}

func mysqlDSN(user, password, host string, port int, dbName string) string {
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8&parseTime=True&loc=Local&multiStatements=true", user, password, host, port, dbName)
}

func mergeEnv(base []string, override map[string]string) []string {
	m := make(map[string]string, len(base)+len(override))
	for _, item := range base {
		parts := strings.SplitN(item, "=", 2)
		if len(parts) != 2 {
			continue
		}
		m[parts[0]] = parts[1]
	}
	for k, v := range override {
		m[k] = v
	}
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}
