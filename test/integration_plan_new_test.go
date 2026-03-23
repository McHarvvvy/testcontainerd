//go:build integration_plan

package test

import (
	"context"
	"testing"
	"time"

	tcd "github.com/McHarvvvy/testcontainerd"
	"github.com/McHarvvvy/testcontainerd/container"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTC01NewFillsDefaultProjectAndRuntimePath(t *testing.T) {
	t.Parallel()

	_, err := tcd.New(tcd.Config{
		Global: tcd.GlobalConfig{},
		Daemon: tcd.DaemonConfig{Addr: "127.0.0.1:0", IdleTTL: 5 * time.Second},
		Client: tcd.ClientConfig{HTTPTimeout: 5 * time.Second},
	}, validRegisterHook("tc01-defaults"))
	require.NoError(t, err)
}

func TestTC02NewRejectsInvalidKeyFieldsExceptGlobalDefaults(t *testing.T) {
	t.Parallel()

	base := tcd.Config{
		Global: tcd.GlobalConfig{},
		Daemon: tcd.DaemonConfig{Addr: "127.0.0.1:0", IdleTTL: 5 * time.Second},
		Client: tcd.ClientConfig{HTTPTimeout: 5 * time.Second},
	}

	tests := []struct {
		name string
		cfg  tcd.Config
	}{
		{
			name: "empty daemon addr",
			cfg: tcd.Config{
				Global: base.Global,
				Daemon: tcd.DaemonConfig{Addr: "", IdleTTL: base.Daemon.IdleTTL},
				Client: base.Client,
			},
		},
		{
			name: "invalid client timeout",
			cfg: tcd.Config{
				Global: base.Global,
				Daemon: base.Daemon,
				Client: tcd.ClientConfig{HTTPTimeout: 0},
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := tcd.New(tt.cfg, validRegisterHook("tc02-"+tt.name))
			assert.Error(t, err)
		})
	}
}

func TestTC03NewRejectsNilRegisterHook(t *testing.T) {
	t.Parallel()

	_, err := tcd.New(tcd.Config{
		Global: tcd.GlobalConfig{},
		Daemon: tcd.DaemonConfig{Addr: "127.0.0.1:0", IdleTTL: 5 * time.Second},
		Client: tcd.ClientConfig{HTTPTimeout: 5 * time.Second},
	}, nil)
	assert.Error(t, err)
}

func TestTC04NewRejectsEmptyContainerRegistrationList(t *testing.T) {
	t.Parallel()

	_, err := tcd.New(tcd.Config{
		Global: tcd.GlobalConfig{},
		Daemon: tcd.DaemonConfig{Addr: "127.0.0.1:0", IdleTTL: 5 * time.Second},
		Client: tcd.ClientConfig{HTTPTimeout: 5 * time.Second},
	}, func(context.Context, tcd.Registrar) error {
		return nil
	})
	assert.Error(t, err)
}

func TestTC07NewRejectsDuplicatedContainerName(t *testing.T) {
	t.Parallel()

	_, err := tcd.New(tcd.Config{
		Global: tcd.GlobalConfig{},
		Daemon: tcd.DaemonConfig{Addr: "127.0.0.1:0", IdleTTL: 5 * time.Second},
		Client: tcd.ClientConfig{HTTPTimeout: 5 * time.Second},
	}, func(ctx context.Context, r tcd.Registrar) error {
		inst := container.MustNewInstance(
			"same-name",
			container.WithType(container.TypeRedis),
			container.WithImage("redis:7.2-alpine"),
			container.WithPort("redis", 6379, 0),
		)
		if err := r.Register(inst); err != nil {
			return err
		}
		return r.Register(inst)
	})
	assert.Error(t, err)
}

func validRegisterHook(name string) tcd.RegisterContainersHook {
	return func(ctx context.Context, r tcd.Registrar) error {
		_ = ctx
		return r.Register(container.MustNewInstance(
			name,
			container.WithType(container.TypeRedis),
			container.WithImage("redis:7.2-alpine"),
			container.WithPort("redis", 6379, 0),
		))
	}
}
