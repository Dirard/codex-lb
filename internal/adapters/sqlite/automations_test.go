package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

func automationStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestAutomationJobRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := automationStore(t)
	saveTestAccount(t, store, "acct_1")
	reasoning := "medium"
	job := domain.AutomationJob{
		ID: "auto_1", Name: "Warmups", Enabled: true, IncludePausedAccounts: true,
		Schedule: domain.AutomationSchedule{Type: "daily", Time: "03:00", Timezone: "UTC", ThresholdMinutes: 15, Days: []string{"mon", "fri"}},
		Model:    "gpt-5.4-mini", ReasoningEffort: &reasoning, Prompt: "ping", AccountIDs: []string{"acct_1"},
		CreatedAt: time.Unix(100, 0), UpdatedAt: time.Unix(200, 0),
	}
	if err := store.SaveAutomationJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.GetAutomationJob(ctx, job.ID)
	if err != nil || loaded.Name != job.Name || !loaded.IncludePausedAccounts || loaded.AccountScopeAll ||
		len(loaded.Schedule.Days) != 2 || loaded.ReasoningEffort == nil || *loaded.ReasoningEffort != "medium" {
		t.Fatalf("loaded = %+v err %v", loaded, err)
	}
	jobs, err := store.ListAutomationJobs(ctx)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs = %+v err %v", jobs, err)
	}
	if err := store.DeleteAutomationJob(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteAutomationJob(ctx, job.ID); err == nil {
		t.Fatal("double delete accepted")
	}
}

func TestAutomationRunClaimIsIdempotentAndStaleSafe(t *testing.T) {
	ctx := context.Background()
	store := automationStore(t)
	accountID := "acct_1"
	run := domain.AutomationRun{
		ID: "run_1", JobID: "auto_1", CycleKey: "scheduled:auto_1:x", SlotKey: "slot_1",
		Trigger: domain.AutomationTriggerScheduled, Model: "m", ScheduledFor: time.Unix(1000, 0), AccountID: &accountID,
	}
	if err := store.EnsureAutomationRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	run.ID = "run_2"
	if err := store.EnsureAutomationRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	runs, total, err := store.ListAutomationRuns(ctx, application.AutomationRunFilter{})
	if err != nil || total != 1 || len(runs) != 1 || runs[0].Status != domain.AutomationPending {
		t.Fatalf("runs = %+v total %d err %v", runs, total, err)
	}
	claimed, err := store.ClaimDueAutomationRuns(ctx, time.Unix(1001, 0))
	if err != nil || len(claimed) != 1 || claimed[0].Status != domain.AutomationRunning || claimed[0].AttemptCount != 1 {
		t.Fatalf("claimed = %+v err %v", claimed, err)
	}
	claimed, err = store.ClaimDueAutomationRuns(ctx, time.Unix(1002, 0))
	if err != nil || len(claimed) != 0 {
		t.Fatalf("fresh running reclaimed = %+v err %v", claimed, err)
	}
	staleBefore := time.Unix(2500, 0)
	// The claim above stamped started_at=now; a claim expires once it is older
	// than the stale threshold, so push the watermark past it.
	staleBefore = time.Now().UTC().Add(time.Hour)
	abandoned, err := store.AbandonStaleAutomationRuns(ctx, staleBefore)
	if err != nil || abandoned != 1 {
		t.Fatalf("stale abandon = %d err %v", abandoned, err)
	}
	afterAbandon, _, err := store.ListAutomationRuns(ctx, application.AutomationRunFilter{})
	if err != nil || afterAbandon[0].Status != domain.AutomationFailed ||
		afterAbandon[0].ErrorCode == nil || *afterAbandon[0].ErrorCode != "stale_run_abandoned" {
		t.Fatalf("abandoned run = %+v err %v", afterAbandon[0], err)
	}
	byCycle, err := store.ListAutomationRunsByCycle(ctx, run.CycleKey)
	if err != nil || len(byCycle) != 1 || byCycle[0].Status != domain.AutomationFailed {
		t.Fatalf("byCycle = %+v err %v", byCycle, err)
	}
}

func TestAutomationRunCompletionIsFencedByGeneration(t *testing.T) {
	ctx := context.Background()
	store := automationStore(t)
	accountID := "acct-generation"
	run := domain.AutomationRun{ID: "run_generation", JobID: "job", CycleKey: "cycle", SlotKey: "slot",
		Trigger: domain.AutomationTriggerManual, Model: "m", ScheduledFor: time.Unix(1000, 0),
		AccountID: &accountID, AccountGeneration: 3}
	if err := store.EnsureAutomationRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimDueAutomationRuns(ctx, time.Unix(1001, 0))
	if err != nil || len(claimed) != 1 || claimed[0].AccountGeneration != 3 {
		t.Fatalf("claimed = %+v %v", claimed, err)
	}
	finished := time.Unix(1002, 0)
	if err := store.CompleteAutomationRun(ctx, run.ID, 4, domain.AutomationSuccess, finished, nil, nil); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("wrong generation completed claim: %v", err)
	}
	if err := store.CompleteAutomationRun(ctx, run.ID, 3, domain.AutomationSuccess, finished, nil, nil); err != nil {
		t.Fatal(err)
	}
	runs, _, err := store.ListAutomationRuns(ctx, application.AutomationRunFilter{})
	if err != nil || len(runs) != 1 || runs[0].Status != domain.AutomationSuccess || runs[0].AccountGeneration != 3 {
		t.Fatalf("runs = %+v %v", runs, err)
	}
}
