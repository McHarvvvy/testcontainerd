package bootstrap

import (
	"context"
	"log"
	"testing"
	"time"

	"github.com/McHarvvvy/testcontainerd"
)

// Run 是各测试包 TestMain 的统一入口。
func Run(m *testing.M) int {
	tcd, err := testcontainerd.New(
		testcontainerd.Config{
			Global: testcontainerd.GlobalConfig{
				Project:     "mssiot_user",
				RuntimePath: "",
			},
			Daemon: testcontainerd.DaemonConfig{
				Addr:    "127.0.0.1:0",
				Token:   "",
				IdleTTL: 36 * time.Second,
			},
			Client: testcontainerd.ClientConfig{
				HTTPTimeout: 1 * time.Minute,
			},
			SUT: newSUTBootPlan(),
		},
		func(ctx context.Context, r testcontainerd.Registrar) error {
			return RegisterContainers(r)
		},
	)
	if err != nil {
		log.Printf("testcontainerd.New failed: %v", err)
		return 1
	}
	return tcd.Run(m)
}
