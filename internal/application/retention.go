package application

import (
	"context"
	"errors"
	"sync"
	"time"

	"codex-lb/internal/domain"
)

// RetentionRepository is the storage side of the retention cycle. Every
// method deletes at most one bounded batch in one batch-writer job.
type RetentionRepository interface {
	LoadSettings(ctx context.Context) (domain.RuntimeSettings, error)
	FoldAndPruneRequestLogs(ctx context.Context, cutoff time.Time, limit int) (int, error)
	PruneAccountQuotaHistory(ctx context.Context, cutoff time.Time, limit int) (int, error)
	ThinAccountQuotaHistory(ctx context.Context, from, to time.Time, limit int) (int, error)
	OldestAccountQuotaObservation(ctx context.Context) (time.Time, error)
	PruneSettledReservations(ctx context.Context, cutoff time.Time, limit int) (int, error)
	PruneAutomationRuns(ctx context.Context, keepCycles, limit int) (int, error)
	PruneLimitWarmupAttempts(ctx context.Context, cutoff time.Time, keep, limit int) (int, error)
	PruneAffinities(ctx context.Context, now time.Time, ttl time.Duration, limit int) (int, error)
	PruneErrorArchives(ctx context.Context, now time.Time) error
}

const (
	// RetentionBatch bounds one delete job on the single SQLite writer.
	RetentionBatch = 500
	// quotaHistoryFullResolution keeps five-minute history for the newest day;
	// older observations are thinned to one per hour.
	quotaHistoryFullResolution = 24 * time.Hour
	settledReservationAge      = 7 * 24 * time.Hour
	automationCyclesKept       = 100
	warmupAttemptsKept         = 100
	warmupAttemptResetAge      = 8 * 24 * time.Hour
)

// RetentionResult summarizes one cycle. Backlog means the budget ended before
// every kind reached a short batch, so the caller should run again soon.
type RetentionResult struct {
	Deleted map[string]int
	Failed  []string
	Backlog bool
}

// RetentionService runs retention as bounded batches until each kind is caught
// up or the cycle budget is spent, so cleanup keeps pace with growth without
// holding the writer.
type RetentionService struct {
	repo RetentionRepository
	now  func() time.Time
	mu   sync.Mutex
	// thinned is the quota-history time below which thinning is complete.
	thinned time.Time
}

func NewRetentionService(repo RetentionRepository, now func() time.Time) *RetentionService {
	if now == nil {
		now = time.Now
	}
	return &RetentionService{repo: repo, now: now}
}

type retentionTask struct {
	name string
	// step deletes one batch and reports whether more work remains.
	step func(context.Context) (int, bool, error)
}

// RunCycle is the workflow "repeat bounded batches through the batch writer
// until a batch is short or the budget elapses". Kinds take turns, one batch
// each, so one large backlog cannot starve the others.
func (s *RetentionService) RunCycle(ctx context.Context, budget time.Duration) (RetentionResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := RetentionResult{Deleted: map[string]int{}}
	settings, err := s.repo.LoadSettings(ctx)
	if err != nil {
		return result, err
	}
	tasks, err := s.tasks(ctx, settings)
	if err != nil {
		return result, err
	}
	started := s.now()
	for len(tasks) > 0 {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		if s.now().Sub(started) >= budget {
			result.Backlog = true
			break
		}
		pending := tasks[:0]
		for _, task := range tasks {
			deleted, more, err := task.step(ctx)
			result.Deleted[task.name] += deleted
			if err != nil {
				if ctx.Err() != nil {
					return result, ctx.Err()
				}
				result.Failed = append(result.Failed, task.name)
				continue
			}
			if more {
				pending = append(pending, task)
			}
		}
		tasks = pending
	}
	if len(result.Failed) > 0 {
		return result, errors.New("some retention kinds failed")
	}
	return result, nil
}

func (s *RetentionService) tasks(ctx context.Context, settings domain.RuntimeSettings) ([]retentionTask, error) {
	now := s.now().UTC()
	batch := func(name string, run func(context.Context) (int, error)) retentionTask {
		return retentionTask{name: name, step: func(ctx context.Context) (int, bool, error) {
			n, err := run(ctx)
			return n, err == nil && n >= RetentionBatch, err
		}}
	}
	var tasks []retentionTask
	if cutoff, ok, err := retentionCutoff(now, settings.EffectiveRequestLogRetentionDays(), domain.MinRequestLogRetentionDays); err != nil {
		return nil, err
	} else if ok {
		tasks = append(tasks, batch("request_logs", func(ctx context.Context) (int, error) {
			return s.repo.FoldAndPruneRequestLogs(ctx, cutoff, RetentionBatch)
		}))
	}
	if cutoff, ok, err := retentionCutoff(now, settings.EffectiveUsageHistoryRetentionDays(), domain.MinUsageHistoryRetentionDays); err != nil {
		return nil, err
	} else if ok {
		tasks = append(tasks, batch("quota_history", func(ctx context.Context) (int, error) {
			return s.repo.PruneAccountQuotaHistory(ctx, cutoff, RetentionBatch)
		}))
		thinning, err := s.thinningTask(ctx, cutoff, now.Add(-quotaHistoryFullResolution))
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, thinning)
	}
	tasks = append(tasks,
		batch("reservations", func(ctx context.Context) (int, error) {
			return s.repo.PruneSettledReservations(ctx, now.Add(-settledReservationAge), RetentionBatch)
		}),
		batch("automation_runs", func(ctx context.Context) (int, error) {
			return s.repo.PruneAutomationRuns(ctx, automationCyclesKept, RetentionBatch)
		}),
		batch("warmup_attempts", func(ctx context.Context) (int, error) {
			return s.repo.PruneLimitWarmupAttempts(ctx, now.Add(-warmupAttemptResetAge), warmupAttemptsKept, RetentionBatch)
		}),
		retentionTask{name: "error_archives", step: func(ctx context.Context) (int, bool, error) {
			return 0, false, s.repo.PruneErrorArchives(ctx, now)
		}},
	)
	if ttl := time.Duration(settings.OpenAICacheAffinityMaxAgeSeconds) * time.Second; ttl > 0 {
		tasks = append(tasks, batch("affinities", func(ctx context.Context) (int, error) {
			return s.repo.PruneAffinities(ctx, now, ttl, RetentionBatch)
		}))
	}
	return tasks, nil
}

// thinningTask walks quota history in one-hour chunks from the oldest retained
// observation (or where the previous cycle stopped) to the full-resolution
// boundary; a chunk is finished once a batch comes back short.
func (s *RetentionService) thinningTask(ctx context.Context, retained, fullResolution time.Time) (retentionTask, error) {
	if s.thinned.IsZero() {
		oldest, err := s.repo.OldestAccountQuotaObservation(ctx)
		if err != nil {
			return retentionTask{}, err
		}
		s.thinned = oldest.Truncate(time.Hour)
	}
	if s.thinned.Before(retained) {
		s.thinned = retained.Truncate(time.Hour)
	}
	return retentionTask{name: "quota_history_thinning", step: func(ctx context.Context) (int, bool, error) {
		if s.thinned.IsZero() || !s.thinned.Before(fullResolution) {
			return 0, false, nil
		}
		end := min(s.thinned.Add(time.Hour).UnixMilli(), fullResolution.UnixMilli())
		chunkEnd := time.UnixMilli(end).UTC()
		n, err := s.repo.ThinAccountQuotaHistory(ctx, s.thinned, chunkEnd, RetentionBatch)
		if err != nil {
			return n, false, err
		}
		if n < RetentionBatch {
			s.thinned = chunkEnd
		}
		return n, s.thinned.Before(fullResolution), nil
	}}, nil
}

// retentionCutoff validates a retention window against its safety floor; zero
// disables the kind.
func retentionCutoff(now time.Time, days, floor int) (time.Time, bool, error) {
	if days == 0 {
		return time.Time{}, false, nil
	}
	if days < floor || days > domain.MaxRetentionDays {
		return time.Time{}, false, domain.ErrInvalid
	}
	return now.AddDate(0, 0, -days), true, nil
}
