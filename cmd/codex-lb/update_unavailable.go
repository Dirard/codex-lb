package main

import (
	"context"
	"log/slog"

	"codex-lb/internal/domain"
)

// Keep ordinary serving available on unsupported hosts, but never substitute
// the bootstrap for a different version selected in a managed installation.
func serveWithoutUpdates(ctx context.Context, cfg config, logger *slog.Logger, reason string) error {
	cfg.updater = unavailableUpdates{reason: reason}
	r, err := openRuntime(ctx, cfg, logger)
	if err != nil {
		return err
	}
	return r.serve(ctx)
}

type unavailableUpdates struct{ reason string }

func (u unavailableUpdates) Status(context.Context) (domain.RuntimeUpdateStatus, error) {
	return domain.RuntimeUpdateStatus{CurrentVersion: version, Phase: "idle", Source: "github", ReleaseURL: "https://github.com/Dirard/codex-lb/releases", UnavailableReason: u.reason}, nil
}
func (u unavailableUpdates) Check(ctx context.Context) (domain.RuntimeUpdateStatus, error) {
	status, _ := u.Status(ctx)
	return status, domain.ErrUpdateUnavailable
}
func (u unavailableUpdates) Apply(ctx context.Context, _ string) (domain.RuntimeUpdateStatus, error) {
	return u.Check(ctx)
}
func (u unavailableUpdates) Rollback(ctx context.Context, _ string) (domain.RuntimeUpdateStatus, error) {
	return u.Check(ctx)
}
