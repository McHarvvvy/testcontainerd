//go:build integration_plan

package test

import (
	"context"
	"errors"
	"testing"
	"time"

	tcd "github.com/McHarvvvy/testcontainerd"
	"github.com/McHarvvvy/testcontainerd/container"
	"github.com/McHarvvvy/testcontainerd/tcdruntime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTC01NewFillsDefaultProjectAndRuntimePath(t *testing.T) {
	tcdInst, err := tcd.New(validConfig(), validRegisterFunc("redis"))
	require.NoError(t, err)
	require.NotNil(t, tcdInst)
	assert.Equal(t, tcdruntime.DefaultRuntimePath("default"), tcdInst.RuntimePath())
}

func TestTC02NewRejectsInvalidKeyFieldsExceptGlobalDefaults(t *testing.T) {
	base := validConfig()
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
		{
			name: "invalid daemon idle ttl",
			cfg: tcd.Config{
				Global: base.Global,
				Daemon: tcd.DaemonConfig{Addr: base.Daemon.Addr, IdleTTL: 0},
				Client: base.Client,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tcd.New(tt.cfg, validRegisterFunc("redis"))
			assert.Error(t, err)
		})
	}
}

func TestTC03NewRejectsNilRegisterContainersFunc(t *testing.T) {
	_, err := tcd.New(validConfig(), nil)
	assert.Error(t, err)
}

func TestTC04NewRejectsEmptyContainerRegistrationList(t *testing.T) {
	_, err := tcd.New(validConfig(), func(context.Context) ([]container.ContainerRegistration, error) {
		return []container.ContainerRegistration{}, nil
	})
	assert.Error(t, err)
}

func TestTC05NewRejectsDuplicatedContainerName(t *testing.T) {
	_, err := tcd.New(validConfig(), func(context.Context) ([]container.ContainerRegistration, error) {
		return []container.ContainerRegistration{
			{Name: "redis", Start: startNoop},
			{Name: "redis", Start: startNoop},
		}, nil
	})
	assert.Error(t, err)
}

func TestTC06NewRejectsInvalidContainerRegistrationFields(t *testing.T) {
	t.Run("name is empty", func(t *testing.T) {
		_, err := tcd.New(validConfig(), func(context.Context) ([]container.ContainerRegistration, error) {
			return []container.ContainerRegistration{{Name: "", Start: startNoop}}, nil
		})
		assert.Error(t, err)
	})

	t.Run("start is nil", func(t *testing.T) {
		_, err := tcd.New(validConfig(), func(context.Context) ([]container.ContainerRegistration, error) {
			return []container.ContainerRegistration{{Name: "redis", Start: nil}}, nil
		})
		assert.Error(t, err)
	})
}

func TestTC07NewPropagatesRegisterContainersError(t *testing.T) {
	expectErr := errors.New("config load failed")
	_, err := tcd.New(validConfig(), func(context.Context) ([]container.ContainerRegistration, error) {
		return nil, expectErr
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, expectErr)
}

func validConfig() tcd.Config {
	return tcd.Config{
		Global: tcd.GlobalConfig{},
		Daemon: tcd.DaemonConfig{Addr: "127.0.0.1:0", IdleTTL: 5 * time.Second},
		Client: tcd.ClientConfig{HTTPTimeout: 5 * time.Second},
	}
}

func validRegisterFunc(name string) tcd.RegisterContainersFunc {
	return func(context.Context) ([]container.ContainerRegistration, error) {
		return []container.ContainerRegistration{{Name: name, Start: startNoop}}, nil
	}
}

func startNoop(context.Context) (container.StartedContainer, error) {
	return container.StartedContainer{SUTEnv: map[string]string{}}, nil
}
