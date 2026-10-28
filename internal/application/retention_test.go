package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

type fakeRetentionRepo struct {
	settings   domain.RuntimeSettings
	now        *time.Time
	logBacklog int
	calls      map[string]int
	failQuota  bool
}

func (f *fakeRetentionRepo) tick(name string) {
	f.calls[name]++
	*f.now = f.now.Add(time.Second)
}
func (f *fakeRetentionRepo) LoadSettings(context.Context) (domain.RuntimeSettings, error) {
	return f.settings, nil
}
func (f *fakeRetentionRepo) FoldAndPruneRequestLogs(context.Context, time.Time, int) (int, error) {
	f.tick("request_logs")
	n := min(f.logBacklog, RetentionBatch)
	f.logBacklog -= n
	return n, nil
}
func (f *fakeRetentionRepo) PruneAccountQuotaHistory(context.Context, time.Time, int) (int, error) {
	f.tick("quota_history")
	if f.failQuota {
		return 0, errors.New("disk I/O error")
	}
	return 3, nil
}
func (f *fakeRetentionRepo) ThinAccountQuotaHistory(context.Context, time.Time, time.Time, int) (int, error) {
	f.tick("thinning")
	return 0, nil
}
func (f *fakeRetentionRepo) OldestAccountQuotaObservation(context.Context) (time.Time, error) {
	return f.now.Add(-48 * time.Hour), nil
}
func (f *fakeRetentionRepo) PruneSettledReservations(context.Context, time.Time, int) (int, error) {
	f.tick("reservations")
	return 1, nil
}
func (f *fakeRetentionRepo) PruneAutomationRuns(context.Context, int, int) (int, error) {
	f.tick("automation_runs")
	return 0, nil
}
func (f *fakeRetentionRepo) PruneLimitWarmupAttempts(context.Context, time.Time, int, int) (int, error) {
	f.tick("warmup_attempts")
	return 0, nil
}
func (f *fakeRetentionRepo) PruneAffinities(context.Context, time.Time, time.Duration, int) (int, error) {
	f.tick("affinities")
	return 0, nil
}
func (f *fakeRetentionRepo) PruneErrorArchives(context.Context, time.Time) error {
	f.tick("error_archives")
	return nil
}

func TestRetentionCycleSharesBudgetAndReportsBacklog(t *testing.T) {
	now := time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC)
	days := 30
	repo := &fakeRetentionRepo{now: &now, logBacklog: 1 << 30, calls: map[string]int{},
		settings: domain.RuntimeSettings{RequestLogRetentionDays: &days, OpenAICacheAffinityMaxAgeSeconds: 60}}
	service := NewRetentionService(repo, func() time.Time { return now })
	result, err := service.RunCycle(context.Background(), 40*time.Second)
	if err != nil || !result.Backlog {
		t.Fatalf("cycle = %+v %v", result, err)
	}
	// Every kind got its turn; only the endless request-log backlog continued.
	for _, name := range []string{"quota_history", "thinning", "reservations", "automation_runs", "warmup_attempts", "affinities", "error_archives"} {
		if repo.calls[name] == 0 {
			t.Fatalf("%s never ran: %v", name, repo.calls)
		}
	}
	if repo.calls["request_logs"] < 10 || repo.calls["quota_history"] != 1 || result.Deleted["request_logs"] != repo.calls["request_logs"]*RetentionBatch {
		t.Fatalf("calls = %v deleted = %v", repo.calls, result.Deleted)
	}
	repo.logBacklog = 10
	result, err = service.RunCycle(context.Background(), time.Hour)
	if err != nil || result.Backlog || result.Deleted["request_logs"] != 10 {
		t.Fatalf("caught-up cycle = %+v %v", result, err)
	}
}

func TestRetentionCycleDefaultsAndFailures(t *testing.T) {
	now := time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC)
	repo := &fakeRetentionRepo{now: &now, calls: map[string]int{}, failQuota: true}
	service := NewRetentionService(repo, func() time.Time { return now })
	result, err := service.RunCycle(context.Background(), time.Hour)
	if err == nil || len(result.Failed) != 1 || result.Failed[0] != "quota_history" {
		t.Fatalf("failure not reported: %+v %v", result, err)
	}
	// No request-log override: events are kept; quota history uses the default.
	if repo.calls["request_logs"] != 0 || repo.calls["quota_history"] != 1 || repo.calls["reservations"] != 1 || repo.calls["affinities"] != 0 {
		t.Fatalf("calls = %v", repo.calls)
	}
	unsafe := 7
	repo.settings.RequestLogRetentionDays = &unsafe
	if _, err := service.RunCycle(context.Background(), time.Hour); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("unsafe retention accepted: %v", err)
	}
	if got := (domain.RuntimeSettings{}).EffectiveUsageHistoryRetentionDays(); got != 45 {
		t.Fatalf("default quota-history retention = %d", got)
	}
}
