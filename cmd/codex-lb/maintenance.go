package main

import (
	"context"
	"errors"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

// Retention runs hourly with a 20 s budget of bounded batches; while a backlog
// remains (for example after an upgrade) it continues every minute.
const (
	retentionInterval        = time.Hour
	retentionBacklogInterval = time.Minute
	retentionBudget          = 20 * time.Second
)

// Reservations waiting for reconciliation are released without charge after
// unreconciledGrace (owner decision): their requests are over and nobody
// reconciles them, while they keep inflating pending request logs.
const (
	unreconciledGrace  = time.Hour
	unreconciledBatch  = 500
	unreconciledBudget = 5 * time.Second
)

// maintainReports runs one maintenance pass; RunPeriodic schedules it every
// minute and reports failures. Retention follows its own budgeted cadence.
func (r *runtime) maintainReports(ctx context.Context) error {
	cleanup, cancel := context.WithTimeout(ctx, 5*time.Second)
	_, err := r.data.store.CleanupDeletedAccounts(cleanup, 1000)
	cancel()
	if releaseErr := r.releaseUnreconciled(ctx); releaseErr != nil && err == nil {
		err = releaseErr
	}
	if r.retention == nil {
		r.retention = application.NewRetentionService(r.data.store, time.Now)
		r.nextOptimize = time.Now().Add(time.Hour)
	}
	// Retention commits batch after batch for up to its budget; while another
	// worker still finishes requests that would starve its settlements.
	if now := time.Now(); !now.Before(r.nextRetention) && r.peerWorkers.Load() == 0 {
		r.nextRetention = now.Add(r.maintainReportRetention(ctx, r.retention))
	}
	r.maintainDatabaseFiles(ctx, &r.nextOptimize)
	return err
}

// releaseUnreconciled releases batches of unreconciled reservations within
// its budget; a backlog continues on the next pass.
func (r *runtime) releaseUnreconciled(ctx context.Context) error {
	deadline := time.Now().Add(unreconciledBudget)
	total := 0
	for time.Now().Before(deadline) {
		batch, cancel := context.WithDeadline(ctx, deadline)
		released, err := r.data.store.ReleaseUnreconciledReservations(batch, time.Now().Add(-unreconciledGrace), unreconciledBatch)
		cancel()
		total += released
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
				break
			}
			return err
		}
		if released < unreconciledBatch {
			break
		}
	}
	if total > 0 {
		r.logger.Info("released unreconciled usage reservations without charge", "count", total)
	}
	return nil
}

// maintainReportRetention runs one budgeted retention cycle and returns the
// delay until the next one.
func (r *runtime) maintainReportRetention(ctx context.Context, retention *application.RetentionService) time.Duration {
	cycle, cancel := context.WithTimeout(ctx, retentionBudget+10*time.Second)
	defer cancel()
	result, err := retention.RunCycle(cycle, retentionBudget)
	if err != nil && ctx.Err() == nil {
		r.logger.Warn("report retention did not complete; will retry next interval", "failed", result.Failed)
	}
	total := 0
	for _, n := range result.Deleted {
		total += n
	}
	if total > 0 || result.Backlog {
		r.logger.Info("retention cycle", "deleted", result.Deleted, "backlog", result.Backlog)
	}
	if result.Backlog {
		return retentionBacklogInterval
	}
	return retentionInterval
}

// maintainDatabaseFiles checkpoints the WAL every cycle (truncating it when
// idle) and refreshes planner statistics once a day.
func (r *runtime) maintainDatabaseFiles(ctx context.Context, nextOptimize *time.Time) {
	checkpoint, cancel := context.WithTimeout(ctx, 5*time.Second)
	if _, err := r.data.store.CheckpointWAL(checkpoint); err != nil && ctx.Err() == nil {
		r.logger.Warn("WAL checkpoint did not complete; will retry next interval")
	}
	cancel()
	if now := time.Now(); !now.Before(*nextOptimize) {
		*nextOptimize = now.Add(24 * time.Hour)
		optimize, cancel := context.WithTimeout(ctx, 5*time.Second)
		if err := r.data.store.Optimize(optimize); err != nil && ctx.Err() == nil {
			r.logger.Warn("planner statistics refresh did not complete; will retry tomorrow")
		}
		cancel()
	}
}

// Scheduled backups wait a few minutes after start so a restart loop does not
// take a snapshot on every start, then check the schedule periodically.
const (
	backupStartDelay    = 5 * time.Minute
	backupCheckInterval = 10 * time.Minute
)

func (r *runtime) maintainBackups(ctx context.Context) {
	timer := time.NewTimer(backupStartDelay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		started := time.Now()
		attempted, file, err := r.backups.CreateIfDue(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			r.logger.Warn("scheduled backup failed; existing backups are kept", "error_code", backupLogCode(err))
		case attempted && err == nil:
			r.logger.Info("backup ok", "name", file.Name, "size_bytes", file.SizeBytes, "duration_ms", time.Since(started).Milliseconds())
		}
		timer.Reset(backupCheckInterval)
	}
}

func backupLogCode(err error) string {
	switch {
	case errors.Is(err, domain.ErrInsufficientStorage):
		return "insufficient_space"
	case errors.Is(err, domain.ErrBackupBusy):
		return "busy"
	default:
		return "backup_failed"
	}
}
