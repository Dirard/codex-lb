package application

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type fakeRollupRepo struct {
	folds              []UsageRollupFold
	foldErr            error
	count, verify      UsageRollupCheck
	batches            []int
	calls              []string
	rebuilding         bool
	verifies, rebuilds int
}

func (r *fakeRollupRepo) FoldUsageRollups(_ context.Context, maxRows, maxJournal int) (UsageRollupFold, error) {
	r.calls = append(r.calls, "fold")
	if maxRows != maxJournal {
		return UsageRollupFold{}, errors.New("uneven limits")
	}
	r.batches = append(r.batches, maxRows)
	if r.foldErr != nil {
		return UsageRollupFold{}, r.foldErr
	}
	if r.rebuilding {
		return UsageRollupFold{Rebuilding: true}, nil
	}
	if len(r.folds) == 0 {
		return UsageRollupFold{}, nil
	}
	fold := r.folds[0]
	r.folds = r.folds[1:]
	return fold, nil
}

func (r *fakeRollupRepo) MarkUsageRollupsRebuilding(context.Context) error {
	r.calls = append(r.calls, "mark")
	r.rebuilding = true
	return nil
}

func (r *fakeRollupRepo) RebuildUsageRollups(context.Context) error {
	r.calls = append(r.calls, "rebuild")
	r.rebuilding = false
	r.rebuilds++
	return nil
}

func (r *fakeRollupRepo) CheckUsageRollupCount(context.Context) (UsageRollupCheck, error) {
	r.calls = append(r.calls, "count")
	return r.count, nil
}

func (r *fakeRollupRepo) VerifyUsageRollups(context.Context, time.Time) (UsageRollupCheck, error) {
	r.calls = append(r.calls, "verify")
	r.verifies++
	return r.verify, nil
}

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

// A backlog folds batch after batch in one run, sized towards 25 ms per job
// within 500..5000 rows, with a PASSIVE checkpoint every 20 batches.
func TestUsageRollupServiceFoldsBacklogInAdaptiveBatches(t *testing.T) {
	repo := &fakeRollupRepo{verify: UsageRollupCheck{Checked: true}, count: UsageRollupCheck{Checked: true}}
	repo.folds = append(repo.folds,
		UsageRollupFold{Rows: 1000, Held: 100 * time.Millisecond, Backlog: true, LagRows: 9000}, // 4x too slow: halve
		UsageRollupFold{Rows: 500, Held: 2 * time.Millisecond, Backlog: true},                   // fast: double
		UsageRollupFold{Rows: 900, Journal: 100, Held: 5 * time.Millisecond, Backlog: true})     // fast: double
	for range 18 {
		repo.folds = append(repo.folds, UsageRollupFold{Rows: 5000, Held: time.Millisecond, Backlog: true})
	}
	repo.folds = append(repo.folds, UsageRollupFold{Rows: 10, LagRows: 0})
	service := NewUsageRollupService(repo, nil, nil)
	checkpoints := 0
	service.Checkpoint = func(context.Context) error { checkpoints++; return nil }
	if err := service.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []int{1000, 500, 1000, 2000, 4000, 5000}; !reflect.DeepEqual(repo.batches[:6], want) || len(repo.batches) != 22 {
		t.Fatalf("batches %v", repo.batches)
	}
	if checkpoints != 1 || repo.verifies != 1 || service.Stats().LagRows != 0 {
		t.Fatalf("checkpoints %d verifies %d stats %+v", checkpoints, repo.verifies, service.Stats())
	}
	if repo.calls[0] != "count" || repo.calls[len(repo.calls)-1] != "verify" {
		t.Fatalf("calls %v", repo.calls)
	}
}

// An inconsistency marks the rollups rebuilding at once and rebuilds; a
// second one within the backoff stays marked (analytics on raw events) until
// the backoff has passed.
func TestUsageRollupServiceRebuildsWithBackoff(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)}
	repo := &fakeRollupRepo{count: UsageRollupCheck{Checked: true, Mismatch: "folded_rows"}, verify: UsageRollupCheck{Checked: true}}
	service := NewUsageRollupService(repo, clock.Now, nil)
	if err := service.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"count", "mark", "rebuild", "fold", "verify"}; !reflect.DeepEqual(repo.calls, want) {
		t.Fatalf("calls %v", repo.calls)
	}
	repo.calls = nil
	repo.folds = []UsageRollupFold{{Rebuild: "anchor"}}
	clock.now = clock.now.Add(time.Minute)
	if err := service.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"fold", "mark"}; !reflect.DeepEqual(repo.calls, want) || !repo.rebuilding {
		t.Fatalf("calls within backoff %v", repo.calls)
	}
	repo.calls = nil
	clock.now = clock.now.Add(usageRollupRebuildBackoff)
	if err := service.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"fold", "rebuild", "fold"}; !reflect.DeepEqual(repo.calls, want) || repo.rebuilding {
		t.Fatalf("calls after backoff %v", repo.calls)
	}
	if stats := service.Stats(); stats.Rebuilds != 2 || stats.FoldErrors != 0 {
		t.Fatalf("stats %+v", stats)
	}
}

// The verifier runs hourly once it could compare; a mismatch is counted and
// rebuilt.
func TestUsageRollupServiceVerifiesHourlyAndRebuildsOnMismatch(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)}
	repo := &fakeRollupRepo{count: UsageRollupCheck{Checked: true}}
	service := NewUsageRollupService(repo, clock.Now, nil)
	run := func() {
		t.Helper()
		if err := service.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	run() // pending journal rows: not checked, retried on the next run
	repo.verify = UsageRollupCheck{Checked: true}
	clock.now = clock.now.Add(2 * time.Second)
	run()
	clock.now = clock.now.Add(30 * time.Minute)
	run()
	if repo.verifies != 2 {
		t.Fatalf("verifies %d", repo.verifies)
	}
	repo.verify = UsageRollupCheck{Checked: true, Mismatch: "usage_rollup_cube_hour"}
	clock.now = clock.now.Add(time.Hour)
	run()
	if stats := service.Stats(); repo.verifies != 3 || stats.Mismatches != 1 || repo.rebuilds != 1 {
		t.Fatalf("verifies %d stats %+v rebuilds %d", repo.verifies, stats, repo.rebuilds)
	}
}

// Fold errors are counted and returned for the background report; a foreign
// layout leaves the rollups alone.
func TestUsageRollupServiceReportsErrorsAndLeavesForeignLayouts(t *testing.T) {
	failure := errors.New("database is locked")
	repo := &fakeRollupRepo{count: UsageRollupCheck{Checked: true}, foldErr: failure}
	service := NewUsageRollupService(repo, nil, nil)
	if err := service.Run(context.Background()); !errors.Is(err, failure) || service.Stats().FoldErrors != 1 {
		t.Fatalf("run %v stats %+v", err, service.Stats())
	}
	repo.foldErr, repo.calls = nil, nil
	repo.folds = []UsageRollupFold{{Foreign: true}}
	if err := service.Run(context.Background()); err != nil || !reflect.DeepEqual(repo.calls, []string{"fold"}) {
		t.Fatalf("foreign layout: %v %v", err, repo.calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	repo.foldErr = context.Canceled
	if err := service.Run(ctx); !errors.Is(err, context.Canceled) || service.Stats().FoldErrors != 1 {
		t.Fatalf("cancelled run %v counted as failure: %+v", err, service.Stats())
	}
}
