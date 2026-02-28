package daemon

import (
	"context"
	"log"

	"github.com/McHarvvvy/testcontainerd/protocol"
)

func (d *Daemon) ensureInfraStarted(ctx context.Context) (map[string]protocol.ResourceEndpoint, error) {
	// 关键决策：基础设施启动入口串行化，避免同一 daemon 被并发 Acquire 重复拉容器。
	d.startMu.Lock()
	defer d.startMu.Unlock()
	if d.infraStarted {
		return d.bundle.Endpoints(), nil
	}
	ep, err := d.bundle.StartAll(ctx)
	if err != nil {
		log.Printf("testcontainerd ensureInfraStarted failed: %v", err)
		return nil, err
	}
	d.infraStarted = true
	return ep, nil
}

func (d *Daemon) stopContainersOnly(ctx context.Context) error {
	d.startMu.Lock()
	defer d.startMu.Unlock()
	if !d.infraStarted {
		return nil
	}
	// 与 ensureInfraStarted 共享同一把锁，确保不会出现“边启动边停止”的交叉状态。
	if err := d.bundle.StopAll(ctx); err != nil {
		return err
	}
	d.infraStarted = false
	return nil
}

func (d *Daemon) ensureSUTStarted(ctx context.Context, resources map[string]protocol.ResourceEndpoint) error {
	return d.sut.ensureStarted(ctx, StartSUTInput{
		Project:     d.cfg.Project,
		RuntimePath: d.cfg.RuntimePath,
		Resources:   resources,
	})
}

func (d *Daemon) stopSUTOnly(ctx context.Context) error {
	return d.sut.stop(ctx)
}
