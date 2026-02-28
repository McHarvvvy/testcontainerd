package bootstrap

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/McHarvvvy/testcontainerd"
	"github.com/McHarvvvy/testcontainerd/container"

	_ "github.com/go-sql-driver/mysql"
)

const (
	topicMemberAccountFlush        = "persistent://meross/bz_normal/q_member_account_flush"
	topicMemberAuthenticationFlush = "persistent://meross/bz_normal/q_member_authentication_flush"
	topicDelayUserTask             = "persistent://meross/delay_user_task/q_user_task"
	topicSMSSendCallback           = "persistent://meross/bz_high/q_sms_notification_send_callback"
	topicSMSNotification           = "persistent://meross/bz_high/q_sms_notification"
	topicSMSNotificationSend       = "persistent://meross/bz_high/q_sms_notification_send"
)

// RegisterContainers 注册默认容器编排。
func RegisterContainers(reg testcontainerd.Registrar) error {
	if reg == nil {
		return fmt.Errorf("registrar is nil")
	}
	if err := reg.Register(container.MustNewInstance(
		"mysql-main",
		container.WithType(container.TypeMySQL),
		container.WithImage("mysql:8.0.36"),
		container.WithPort("mysql", 3306, 0),
		container.WithEnv("MYSQL_ROOT_PASSWORD", "pass"),
		container.WithInit(initMySQL),
	)); err != nil {
		return err
	}

	if err := reg.Register(container.MustNewInstance(
		"redis-main",
		container.WithType(container.TypeRedis),
		container.WithImage("redis:7.2-alpine"),
		container.WithPort("redis", 6379, 0),
	)); err != nil {
		return err
	}

	if err := reg.Register(container.MustNewInstance(
		"mongo-main",
		container.WithType(container.TypeMongo),
		container.WithImage("mongo:6.0"),
		container.WithPort("mongo", 27017, 0),
		container.WithEnv("MONGO_INITDB_ROOT_USERNAME", "root"),
		container.WithEnv("MONGO_INITDB_ROOT_PASSWORD", "pass"),
	)); err != nil {
		return err
	}

	if err := reg.Register(container.MustNewInstance(
		"pulsar-main",
		container.WithType(container.TypePulsar),
		container.WithImage("apachepulsar/pulsar:3.1.1"),
		container.WithPort("service", 6650, 0),
		container.WithPort("admin", 8080, 0),
		container.WithInit(initPulsar),
	)); err != nil {
		return err
	}

	return nil
}

func initMySQL(ctx context.Context, in container.InitInput) error {
	port, ok := in.Self.Ports["mysql"]
	if !ok {
		return fmt.Errorf("mysql-main mysql port not found")
	}
	user := in.Self.Metadata["user"]
	if user == "" {
		user = "root"
	}
	password := in.Self.Metadata["password"]
	if password == "" {
		password = "pass"
	}
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/?charset=utf8&parseTime=True&loc=Local&multiStatements=true", user, password, in.Self.Host, port)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return err
	}
	defer db.Close()

	pingCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err = db.PingContext(pingCtx); err != nil {
		return err
	}

	for _, dbName := range []string{"meross_user", "meross_smart", "global_common_config", "global_common_data", "global_share_resource", "meross_journal"} {
		if _, err = db.ExecContext(ctx, fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s`", dbName)); err != nil {
			return err
		}
	}

	createAppH5ConfigTable := `CREATE TABLE IF NOT EXISTS global_common_data.m_app_h5_config (
	id INT NOT NULL AUTO_INCREMENT,
	vendor VARCHAR(64) NOT NULL DEFAULT '',
	create_time INT NOT NULL DEFAULT 0,
	update_time INT NOT NULL DEFAULT 0,
	device_type VARCHAR(64) NOT NULL DEFAULT '',
	sub_type VARCHAR(64) NOT NULL DEFAULT '',
	is_enable TINYINT NOT NULL DEFAULT 1,
	config TEXT,
	PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;`
	if _, err = db.ExecContext(ctx, createAppH5ConfigTable); err != nil {
		return err
	}
	return nil
}

func initPulsar(ctx context.Context, in container.InitInput) error {
	adminPort, ok := in.Self.Ports["admin"]
	if !ok {
		return fmt.Errorf("pulsar-main admin port not found")
	}
	baseURL := fmt.Sprintf("http://%s:%d", in.Self.Host, adminPort)

	if err := pulsarPut(ctx, baseURL, "/admin/v2/tenants/meross", map[string]interface{}{
		"allowedClusters": []string{"standalone"},
		"adminRoles":      []string{},
	}); err != nil {
		return err
	}
	for _, ns := range []string{"bz_normal", "bz_high", "delay_user_task"} {
		if err := pulsarPut(ctx, baseURL, "/admin/v2/namespaces/meross/"+ns, nil); err != nil {
			return err
		}
	}
	for _, topic := range []string{
		topicMemberAccountFlush,
		topicMemberAuthenticationFlush,
		topicDelayUserTask,
		topicSMSSendCallback,
		topicSMSNotification,
		topicSMSNotificationSend,
	} {
		path, ok := pulsarPersistentPath(topic)
		if !ok {
			continue
		}
		if err := pulsarPut(ctx, baseURL, "/admin/v2/persistent/"+path, nil); err != nil {
			return err
		}
	}
	return nil
}

func pulsarPut(ctx context.Context, baseURL, path string, payload interface{}) error {
	var body []byte
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = b
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	if resp.StatusCode == http.StatusConflict || resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusPreconditionFailed || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return fmt.Errorf("pulsar admin put failed: %s status=%d", path, resp.StatusCode)
}

func pulsarPersistentPath(topic string) (string, bool) {
	if !strings.HasPrefix(topic, "persistent://") {
		return "", false
	}
	return strings.TrimPrefix(topic, "persistent://"), true
}
