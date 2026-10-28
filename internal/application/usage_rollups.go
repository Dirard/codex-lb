package application

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// UsageRollupFold is the outcome of one folder job.
type UsageRollupFold struct {
	// Rows and Journal count the events and journal rows folded.
	Rows, Journal int
	// Held is the job's execution time inside the write transaction.
	Held time.Duration
	// Backlog means events after the cursor or journal rows remain.
	Backlog bool
	// LagRows estimates the events after the cursor plus the journal rows
	// from their rowid and sequence ranges.
	LagRows int64
	// Rebuild names the inconsistency that requires a rebuild; the job then
	// folded nothing.
	Rebuild string
	// Rebuilding means a rebuild is in progress; the job folded nothing.
	Rebuilding bool
	// Foreign means the rollups have a layout this code neither folds nor
	// reads; the release that wrote it owns them.
	Foreign bool
}

// UsageRollupCheck is the outcome of a read-only consistency check.
type UsageRollupCheck struct {
	// Checked is false when the rollups were not ready or the check could
	// not compare a consistent state; it should run again later.
	Checked bool
	// Mismatch names the first table or read path that differs.
	Mismatch string
}

// UsageRollupRepository is the storage side of the usage rollup folder. Fold
// and rebuild steps are bounded batch-writer jobs; checks only read.
type UsageRollupRepository interface {
	FoldUsageRollups(ctx context.Context, maxRows, maxJournal int) (UsageRollupFold, error)
	MarkUsageRollupsRebuilding(ctx context.Context) error
	RebuildUsageRollups(ctx context.Context) error
	CheckUsageRollupCount(ctx context.Context) (UsageRollupCheck, error)
	VerifyUsageRollups(ctx context.Context, now time.Time) (UsageRollupCheck, error)
}

const (
	// usageRollupInterval is how often the folder looks for new events.
	usageRollupInterval = 2 * time.Second
	// Batches adapt between these sizes towards usageRollupTargetJob per job,
	// well below the writer's execution timeout and lock share.
	usageRollupMinBatch  = 500
	usageRollupMaxBatch  = 5000
	usageRollupFirstJob  = 1000
	usageRollupTargetJob = 25 * time.Millisecond
	// usageRollupMinPause separates backlog batches; a pause at least as long
	// as the batch keeps the folder's share of the writer at most a half.
	usageRollupMinPause = 10 * time.Millisecond
	// usageRollupCheckpointEvery batches of a backlog run a PASSIVE checkpoint.
	usageRollupCheckpointEvery = 20
	usageRollupVerifyInterval  = time.Hour
	// usageRollupRebuildBackoff spaces rebuilds: should one inconsistency keep
	// recurring, analytics stay on raw events instead of rebuilding nonstop.
	usageRollupRebuildBackoff = 10 * time.Minute
	// usageRollupLogRows is the backlog size worth an informational log line.
	usageRollupLogRows = 10000
)

// UsageRollupStats are the folder's counters since start.
type UsageRollupStats struct {
	LagRows, FoldErrors, Rebuilds, Mismatches int64
}

// UsageRollupService keeps usage rollups caught up with usage events. Only the
// active worker of an installation runs it; inserts of any worker, journaled
// changes and an upgrade's backlog are folded the same way.
type UsageRollupService struct {
	repo UsageRollupRepository
	now  func() time.Time
	log  *slog.Logger
	// Checkpoint, when set, runs a PASSIVE WAL checkpoint during a backlog.
	Checkpoint func(context.Context) error

	mu          sync.Mutex
	batch       int
	counted     bool
	nextVerify  time.Time
	lastRebuild time.Time

	lag, foldErrors, rebuilds, mismatches atomic.Int64
}

func NewUsageRollupService(repo UsageRollupRepository, now func() time.Time, logger *slog.Logger) *UsageRollupService {
	if now == nil {
		now = time.Now
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &UsageRollupService{repo: repo, now: now, log: logger, batch: usageRollupFirstJob}
}

// RunPeriodic runs the folder every two seconds until ctx ends.
func (s *UsageRollupService) RunPeriodic(ctx context.Context, schedule BackgroundSchedule) {
	RunPeriodic(ctx, "usage_rollups", schedule, usageRollupInterval, nil, s.Run)
}

// Stats returns the folder's counters; safe for concurrent use.
func (s *UsageRollupService) Stats() UsageRollupStats {
	return UsageRollupStats{LagRows: s.lag.Load(), FoldErrors: s.foldErrors.Load(),
		Rebuilds: s.rebuilds.Load(), Mismatches: s.mismatches.Load()}
}

// Run is the workflow "check the folded row count once --> fold batches until
// no backlog, pausing between them --> [inconsistent --mark rebuilding, clear
// rollups--> backfill] --> hourly verifier". Every fold is one atomic job, so
// an error or crash leaves the previous state for the next run.
func (s *UsageRollupService) Run(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.counted {
		// VACUUM or a restored backup may renumber rowids under the cursor.
		check, err := s.repo.CheckUsageRollupCount(ctx)
		if err != nil {
			return s.failed(ctx, err)
		}
		s.counted = true
		if check.Mismatch != "" {
			if done, err := s.rebuild(ctx, check.Mismatch); err != nil || !done {
				return err
			}
		}
	}
	started := s.now()
	folded, journaled, batches := 0, 0, 0
	for ctx.Err() == nil {
		fold, err := s.repo.FoldUsageRollups(ctx, s.batch, s.batch)
		if err != nil {
			return s.failed(ctx, err)
		}
		s.lag.Store(fold.LagRows)
		switch {
		case fold.Foreign:
			return nil
		case fold.Rebuild != "":
			if done, err := s.rebuild(ctx, fold.Rebuild); err != nil || !done {
				return err
			}
			continue
		case fold.Rebuilding:
			if done, err := s.finishRebuild(ctx); err != nil || !done {
				return err
			}
			continue
		}
		folded, journaled, batches = folded+fold.Rows, journaled+fold.Journal, batches+1
		if !fold.Backlog {
			break
		}
		s.adapt(fold)
		if batches%usageRollupCheckpointEvery == 0 && s.Checkpoint != nil {
			_ = s.Checkpoint(ctx)
		}
		pause := time.NewTimer(max(usageRollupMinPause, fold.Held))
		select {
		case <-ctx.Done():
			pause.Stop()
		case <-pause.C:
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if folded+journaled >= usageRollupLogRows {
		s.log.Info("usage rollups folded", "rows", folded, "journal_rows", journaled, "batches", batches,
			"batch_rows", s.batch, "duration_ms", s.now().Sub(started).Milliseconds())
	}
	return s.verifyIfDue(ctx)
}

// adapt sizes the next batch so a job takes about usageRollupTargetJob,
// changing at most twofold per batch.
func (s *UsageRollupService) adapt(fold UsageRollupFold) {
	items := fold.Rows + fold.Journal
	if items == 0 {
		return
	}
	perItem := float64(max(fold.Held, time.Microsecond)) / float64(items)
	next := min(max(float64(usageRollupTargetJob)/perItem, float64(s.batch)/2), float64(s.batch)*2)
	s.batch = min(max(int(next), usageRollupMinBatch), usageRollupMaxBatch)
}

// rebuild marks the rollups rebuilding at once, so analytics read raw events,
// and clears them unless a rebuild finished within the backoff.
func (s *UsageRollupService) rebuild(ctx context.Context, reason string) (bool, error) {
	s.log.Warn("usage rollups rebuilding; analytics read raw events meanwhile", "reason", reason)
	if err := s.repo.MarkUsageRollupsRebuilding(ctx); err != nil {
		return false, s.failed(ctx, err)
	}
	s.rebuilds.Add(1)
	return s.finishRebuild(ctx)
}

func (s *UsageRollupService) finishRebuild(ctx context.Context) (bool, error) {
	if !s.lastRebuild.IsZero() && s.now().Sub(s.lastRebuild) < usageRollupRebuildBackoff {
		return false, nil
	}
	if err := s.repo.RebuildUsageRollups(ctx); err != nil {
		return false, s.failed(ctx, err)
	}
	s.lastRebuild = s.now()
	return true, nil
}

// verifyIfDue compares the rollups with raw events hourly; a mismatch is
// logged, counted and rebuilt.
func (s *UsageRollupService) verifyIfDue(ctx context.Context) error {
	if s.now().Before(s.nextVerify) {
		return nil
	}
	check, err := s.repo.VerifyUsageRollups(ctx, s.now())
	if err != nil {
		return s.failed(ctx, err)
	}
	if !check.Checked {
		return nil
	}
	s.nextVerify = s.now().Add(usageRollupVerifyInterval)
	if check.Mismatch == "" {
		return nil
	}
	s.mismatches.Add(1)
	s.log.Warn("usage_rollup_mismatch", "check", check.Mismatch)
	_, err = s.rebuild(ctx, "verifier: "+check.Mismatch)
	return err
}

func (s *UsageRollupService) failed(ctx context.Context, err error) error {
	if ctx.Err() == nil {
		s.foldErrors.Add(1)
	}
	return err
}
