package main

import (
	"context"
	"errors"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

func (r *runtime) maintainReports(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	var nextRetention time.Time
	for {
		cleanup, cancel := context.WithTimeout(ctx, 5*time.Second)
		if _, err := r.data.store.CleanupDeletedAccounts(cleanup, 1000); err != nil && ctx.Err() == nil {
			r.logger.Warn("deleted-account cleanup did not complete; will retry next interval")
		}
		cancel()
		if now := time.Now(); !now.Before(nextRetention) {
			r.maintainReportRetention(ctx)
			nextRetention = now.Add(time.Hour)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *runtime) maintainReportRetention(ctx context.Context) {
	service := application.NewReportsService(r.data.store, nil)
	cycle, cancel := context.WithTimeout(ctx, 30*time.Second)
	settings, err := r.data.store.LoadSettings(cycle)
	if err == nil && settings.RequestLogRetentionDays != nil {
		// A bounded batch leaves the single SQLite writer available for accounting.
		_, err = service.PruneRequestLogs(cycle, *settings.RequestLogRetentionDays)
	}
	if err == nil && settings.UsageHistoryRetentionDays != nil {
		_, err = service.PruneAccountQuotaHistory(cycle, *settings.UsageHistoryRetentionDays)
	}
	if err == nil {
		_, err = r.data.store.PruneAffinities(cycle, time.Now().UTC(), time.Duration(settings.OpenAICacheAffinityMaxAgeSeconds)*time.Second, 1000)
	}
	cancel()
	if err != nil && ctx.Err() == nil {
		r.logger.Warn("report retention did not complete; will retry next interval")
	}
	archiveCtx, cancelArchive := context.WithTimeout(ctx, 5*time.Second)
	defer cancelArchive()
	if err := r.data.store.PruneErrorArchives(archiveCtx, time.Now().UTC()); err != nil && ctx.Err() == nil {
		r.logger.Warn("error-diagnostic retention did not complete; will retry next interval")
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
