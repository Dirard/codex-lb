package application

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

type fakeAutomationsStore struct {
	mu     sync.Mutex
	jobs   map[string]domain.AutomationJob
	runs   map[string]domain.AutomationRun
	bySlot map[string]string
}

func newFakeAutomationsStore() *fakeAutomationsStore {
	return &fakeAutomationsStore{jobs: map[string]domain.AutomationJob{}, runs: map[string]domain.AutomationRun{}, bySlot: map[string]string{}}
}

func (f *fakeAutomationsStore) SaveAutomationJob(_ context.Context, job domain.AutomationJob) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jobs[job.ID] = job
	return nil
}

func (f *fakeAutomationsStore) GetAutomationJob(_ context.Context, id string) (domain.AutomationJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	job, ok := f.jobs[id]
	if !ok {
		return job, domain.ErrNotFound
	}
	return job, nil
}

func (f *fakeAutomationsStore) ListAutomationJobs(context.Context) ([]domain.AutomationJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]domain.AutomationJob, 0, len(f.jobs))
	for _, job := range f.jobs {
		result = append(result, job)
	}
	return result, nil
}

func (f *fakeAutomationsStore) DeleteAutomationJob(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.jobs[id]; !ok {
		return domain.ErrNotFound
	}
	delete(f.jobs, id)
	return nil
}

func (f *fakeAutomationsStore) EnsureAutomationRun(_ context.Context, run domain.AutomationRun) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.bySlot[run.SlotKey]; exists {
		return nil
	}
	run.Status = domain.AutomationPending
	f.runs[run.ID] = run
	f.bySlot[run.SlotKey] = run.ID
	return nil
}

func (f *fakeAutomationsStore) ClaimDueAutomationRuns(_ context.Context, dueBefore time.Time) ([]domain.AutomationRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var claimed []domain.AutomationRun
	for id, run := range f.runs {
		due := run.Status == domain.AutomationPending && !run.ScheduledFor.After(dueBefore)
		if !due {
			continue
		}
		now := time.Now().UTC()
		run.Status = domain.AutomationRunning
		run.StartedAt = &now
		run.AttemptCount++
		f.runs[id] = run
		claimed = append(claimed, run)
	}
	return claimed, nil
}

func (f *fakeAutomationsStore) AbandonStaleAutomationRuns(_ context.Context, staleBefore time.Time) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	abandoned := 0
	for id, run := range f.runs {
		if run.Status != domain.AutomationRunning || run.StartedAt == nil || !run.StartedAt.Before(staleBefore) {
			continue
		}
		finished := staleBefore
		code, message := "stale_run_abandoned", "Run claim expired after a crash; not replayed automatically"
		run.Status = domain.AutomationFailed
		run.FinishedAt = &finished
		run.ErrorCode, run.ErrorMessage = &code, &message
		f.runs[id] = run
		abandoned++
	}
	return abandoned, nil
}

func (f *fakeAutomationsStore) CompleteAutomationRun(_ context.Context, id string, generation int64, status string, finishedAt time.Time, errorCode, errorMessage *string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	run, ok := f.runs[id]
	if !ok {
		return domain.ErrNotFound
	}
	if run.AccountGeneration != generation {
		return domain.ErrConflict
	}
	run.Status = status
	run.FinishedAt = &finishedAt
	run.ErrorCode = errorCode
	run.ErrorMessage = errorMessage
	f.runs[id] = run
	return nil
}

func (f *fakeAutomationsStore) ListAutomationRuns(_ context.Context, filter AutomationRunFilter) ([]domain.AutomationRun, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]domain.AutomationRun, 0, len(f.runs))
	for _, run := range f.runs {
		result = append(result, run)
	}
	return result, len(result), nil
}

func (f *fakeAutomationsStore) LatestAutomationRunByJob(context.Context) (map[string]domain.AutomationRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := map[string]domain.AutomationRun{}
	for _, run := range f.runs {
		current, ok := result[run.JobID]
		if !ok || run.ScheduledFor.After(current.ScheduledFor) {
			result[run.JobID] = run
		}
	}
	return result, nil
}

func (f *fakeAutomationsStore) ListAutomationRunsByCycle(_ context.Context, cycleKey string) ([]domain.AutomationRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var result []domain.AutomationRun
	for _, run := range f.runs {
		if run.CycleKey == cycleKey {
			result = append(result, run)
		}
	}
	return result, nil
}

type stubWarmupExecutor struct {
	mu      sync.Mutex
	calls   []string
	failFor map[string]error
}

func (s *stubWarmupExecutor) ExecuteWarmupForGeneration(_ context.Context, accountID string, _ int64, model string, _ *string, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, accountID+"@"+model)
	if err, ok := s.failFor[accountID]; ok {
		return err
	}
	return nil
}

func newAutomationsTestService(t *testing.T) (*AutomationsService, *fakeAutomationsStore, *fakeAccountsStore, *stubWarmupExecutor, *time.Time) {
	t.Helper()
	store := newFakeAutomationsStore()
	accounts := newFakeAccounts()
	warmups := &stubWarmupExecutor{}
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	service := NewAutomationsService(store, accounts, warmups, func() time.Time { return now })
	return service, store, accounts, warmups, &now
}

func automationTestJob(name string) domain.AutomationJob {
	return domain.AutomationJob{
		Name: name, Enabled: true, Model: "gpt-5.4-mini",
		Schedule: domain.AutomationSchedule{
			Type: "daily", Time: "03:00", Timezone: "UTC", Days: []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"},
		},
	}
}

func TestAutomationsJobValidationAndDefaults(t *testing.T) {
	ctx := context.Background()
	service, _, _, _, _ := newAutomationsTestService(t)
	job, err := service.CreateJob(ctx, automationTestJob("Warmups"))
	if err != nil {
		t.Fatal(err)
	}
	if job.Prompt != domain.DefaultAutomationPrompt || !job.AccountScopeAll || job.ID == "" {
		t.Fatalf("job defaults = %+v", job)
	}
	invalid := automationTestJob("Bad")
	invalid.Schedule.Time = "25:99"
	if _, err := service.CreateJob(ctx, invalid); err == nil {
		t.Fatal("invalid time accepted")
	}
	invalid = automationTestJob("Bad")
	invalid.Schedule.ThresholdMinutes = 241
	if _, err := service.CreateJob(ctx, invalid); err == nil {
		t.Fatal("invalid threshold accepted")
	}
	invalid = automationTestJob("Bad")
	invalid.Schedule.Days = []string{"mon", "mon"}
	if _, err := service.CreateJob(ctx, invalid); err == nil {
		t.Fatal("duplicate days accepted")
	}
	invalid = automationTestJob("Bad")
	invalid.ReasoningEffort = stringPtr("banana")
	if _, err := service.CreateJob(ctx, invalid); err == nil {
		t.Fatal("invalid reasoning accepted")
	}
}

func TestLatestDueSlotAndNextRunRespectTimezoneAndDays(t *testing.T) {
	schedule := domain.AutomationSchedule{Time: "03:00", Timezone: "Europe/Moscow", Days: []string{"mon"}}
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC) // Saturday
	due := LatestDueSlotUTC(now, schedule)
	if _, offset := due.Zone(); offset != 0 {
		t.Fatalf("due not UTC: %v", due)
	}
	// Saturday 12:00 UTC = 14:00 MSK; latest Monday slot is Sep 21 03:00 MSK.
	if want := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC); !due.Equal(want) {
		t.Fatalf("due = %v want %v", due, want)
	}
	next := NextRunUTC(now, schedule)
	if want := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC); !next.Equal(want) {
		t.Fatalf("next = %v want %v", next, want)
	}
}

func TestRunDueJobsIsRestartSafeAndTargetsEligibleAccounts(t *testing.T) {
	ctx := context.Background()
	service, store, accounts, warmups, now := newAutomationsTestService(t)
	// Job must predate the due slot, otherwise the no-catch-up guard skips it.
	*now = time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	active := domain.Account{ID: "acct_active", Kind: domain.AccountChatGPT, Provider: "openai", ChatGPTAccountID: "cg1", Email: "a@example.test", PlanType: "plus", RoutingPolicy: "normal", Status: domain.AccountActive, CreatedAt: time.Unix(1, 0)}
	paused := active
	paused.ID, paused.ChatGPTAccountID, paused.Email = "acct_paused", "cg2", "p@example.test"
	paused.Status = domain.AccountPaused
	rateLimited := active
	rateLimited.ID, rateLimited.ChatGPTAccountID, rateLimited.Email = "acct_rl", "cg3", "r@example.test"
	rateLimited.Status = domain.AccountRateLimited
	for _, account := range []domain.Account{active, paused, rateLimited} {
		if err := accounts.SaveAccount(ctx, account); err != nil {
			t.Fatal(err)
		}
	}
	job, err := service.CreateJob(ctx, automationTestJob("Warmups"))
	if err != nil {
		t.Fatal(err)
	}
	*now = time.Date(2026, 9, 26, 3, 0, 1, 0, time.UTC)
	if _, err := service.RunDueJobs(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RunDueJobs(ctx); err != nil {
		t.Fatal(err)
	}
	if len(warmups.calls) != 1 || warmups.calls[0] != "acct_active@gpt-5.4-mini" {
		t.Fatalf("warmup calls = %+v", warmups.calls)
	}
	runs, _ := store.ListAutomationRunsByCycle(ctx, latestCycleKey(t, store, job.ID))
	if len(runs) != 1 || runs[0].Status != domain.AutomationSuccess {
		t.Fatalf("runs = %+v", runs)
	}
}

func TestClaimedAutomationDoesNotDispatchToReimportedAccount(t *testing.T) {
	ctx := context.Background()
	service, store, accounts, warmups, now := newAutomationsTestService(t)
	*now = time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	account := domain.Account{ID: "acct_reimport", Kind: domain.AccountChatGPT, Provider: "openai",
		ChatGPTAccountID: "cg-reimport", Email: "r@example.test", PlanType: "plus", RoutingPolicy: "normal",
		Status: domain.AccountActive, CreatedAt: time.Unix(1, 0)}
	if err := accounts.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	job, err := service.CreateJob(ctx, automationTestJob("Reimport guard"))
	if err != nil {
		t.Fatal(err)
	}
	*now = time.Date(2026, 9, 26, 3, 0, 0, 0, time.UTC)
	if err := service.ensureCycleRuns(ctx, job, "synthetic:reimport", domain.AutomationTriggerManual, *now); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimDueAutomationRuns(ctx, now.Add(time.Second))
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claims = %+v %v", claimed, err)
	}
	account.Generation = 1
	if err := accounts.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	service.executeClaimed(ctx, job, claimed[0])
	if len(warmups.calls) != 0 {
		t.Fatalf("old automation dispatched to reimport: %+v", warmups.calls)
	}
	store.mu.Lock()
	run := store.runs[claimed[0].ID]
	store.mu.Unlock()
	if run.Status != domain.AutomationFailed || run.AccountGeneration != 0 || run.ErrorCode == nil || *run.ErrorCode != "account_ineligible" {
		t.Fatalf("stale claim completion = %+v", run)
	}
}

func latestCycleKey(t *testing.T, store *fakeAutomationsStore, jobID string) string {
	t.Helper()
	runs, _, _ := store.ListAutomationRuns(context.Background(), AutomationRunFilter{})
	var latest string
	for _, run := range runs {
		if run.JobID == jobID && (latest == "" || run.CycleKey > latest) {
			latest = run.CycleKey
		}
	}
	if latest == "" {
		t.Fatal("no runs")
	}
	return latest
}

func TestRunDueJobsStaggerAndPartialRollup(t *testing.T) {
	ctx := context.Background()
	service, store, accounts, warmups, now := newAutomationsTestService(t)
	*now = time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	job := automationTestJob("Stagger")
	job.Schedule.ThresholdMinutes = 60
	job, err := service.CreateJob(ctx, job)
	if err != nil {
		t.Fatal(err)
	}
	for index := range 3 {
		account := domain.Account{
			ID: "acct_" + string(rune('a'+index)), Kind: domain.AccountChatGPT, Provider: "openai",
			ChatGPTAccountID: "cg", Email: string(rune('a'+index)) + "@example.test", PlanType: "plus",
			RoutingPolicy: "normal", Status: domain.AccountActive, CreatedAt: time.Unix(1, 0),
		}
		if err := accounts.SaveAccount(ctx, account); err != nil {
			t.Fatal(err)
		}
	}
	warmups.failFor = map[string]error{"acct_b": errors.New("synthetic warmup rejected")}
	*now = time.Date(2026, 9, 26, 3, 59, 0, 0, time.UTC)
	if _, err := service.RunDueJobs(ctx); err != nil {
		t.Fatal(err)
	}
	runs, _ := store.ListAutomationRunsByCycle(ctx, latestCycleKey(t, store, job.ID))
	if len(runs) != 3 {
		t.Fatalf("runs = %+v", runs)
	}
	for _, run := range runs {
		if run.ScheduledFor.After(*now) {
			if run.Status != domain.AutomationPending {
				t.Fatalf("future stagger executed early: %+v", run)
			}
			continue
		}
	}
	*now = now.Add(2 * time.Minute)
	if _, err := service.RunDueJobs(ctx); err != nil {
		t.Fatal(err)
	}
	runs, _ = store.ListAutomationRunsByCycle(ctx, latestCycleKey(t, store, job.ID))
	rollup := CycleRollup(runs)
	if rollup.Status != domain.AutomationPartial {
		t.Fatalf("rollup = %+v", rollup)
	}
}

func TestRunNowExecutesManualCycleImmediately(t *testing.T) {
	ctx := context.Background()
	service, _, accounts, warmups, _ := newAutomationsTestService(t)
	account := domain.Account{ID: "acct_manual", Kind: domain.AccountChatGPT, Provider: "openai", ChatGPTAccountID: "cg", Email: "m@example.test", PlanType: "plus", RoutingPolicy: "normal", Status: domain.AccountActive, CreatedAt: time.Unix(1, 0)}
	if err := accounts.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	job, err := service.CreateJob(ctx, automationTestJob("Manual"))
	if err != nil {
		t.Fatal(err)
	}
	run, err := service.RunNow(ctx, job.ID)
	if err != nil || run.Status != domain.AutomationSuccess {
		t.Fatalf("run = %+v err %v", run, err)
	}
	if len(warmups.calls) != 1 {
		t.Fatalf("warmup calls = %+v", warmups.calls)
	}
}

func TestCrashedRunningRunsAreAbandonedNotReplayed(t *testing.T) {
	ctx := context.Background()
	service, store, accounts, warmups, _ := newAutomationsTestService(t)
	account := domain.Account{ID: "acct_crash", Kind: domain.AccountChatGPT, Provider: "openai", ChatGPTAccountID: "cg", Email: "c@example.test", PlanType: "plus", RoutingPolicy: "normal", Status: domain.AccountActive, CreatedAt: time.Unix(1, 0)}
	if err := accounts.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	job, err := service.CreateJob(ctx, automationTestJob("Crash"))
	if err != nil {
		t.Fatal(err)
	}
	accountID := account.ID
	oldStart := time.Unix(1, 0)
	crashed := domain.AutomationRun{
		ID: "run_crashed", JobID: job.ID, CycleKey: "scheduled:" + job.ID + ":crash", SlotKey: "slot_crashed",
		Trigger: domain.AutomationTriggerScheduled, Status: domain.AutomationRunning, Model: job.Model,
		ScheduledFor: oldStart, StartedAt: &oldStart, AccountID: &accountID,
	}
	if err := store.EnsureAutomationRun(ctx, crashed); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	saved := store.runs[crashed.ID]
	saved.Status, saved.StartedAt = domain.AutomationRunning, &oldStart
	store.runs[crashed.ID] = saved
	store.mu.Unlock()

	if _, err := service.RunDueJobs(ctx); err != nil {
		t.Fatal(err)
	}
	final := store.runs[crashed.ID]
	if final.Status != domain.AutomationFailed || final.ErrorCode == nil || *final.ErrorCode != "stale_run_abandoned" {
		t.Fatalf("crashed run = %+v", final)
	}
	// The fresh latest-slot cycle may legitimately execute once; the crashed
	// slot itself must never be replayed.
	if len(warmups.calls) > 1 {
		t.Fatalf("unexpected replay count: %+v", warmups.calls)
	}
}
