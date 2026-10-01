package application

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"codex-lb/internal/domain"
)

const (
	automationMaxThresholdMinutes = 240
	automationMaxNameLength       = 200
	automationMaxPromptLength     = 1000
	automationStaleRunAge         = 10 * time.Minute
	automationPollerInterval      = 30 * time.Second
)

var automationWeekdays = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

var automationReasoningEfforts = map[string]bool{
	"minimal": true, "low": true, "medium": true, "high": true, "xhigh": true, "max": true, "ultra": true,
}

type AutomationRunFilter struct {
	JobIDs     []string
	AccountIDs []string
	Models     []string
	Statuses   []string
	Triggers   []string
	Search     string
	Limit      int
	Offset     int
}

type AutomationsStore interface {
	SaveAutomationJob(ctx context.Context, job domain.AutomationJob) error
	GetAutomationJob(ctx context.Context, id string) (domain.AutomationJob, error)
	ListAutomationJobs(ctx context.Context) ([]domain.AutomationJob, error)
	DeleteAutomationJob(ctx context.Context, id string) error
	EnsureAutomationRun(ctx context.Context, run domain.AutomationRun) error
	ClaimDueAutomationRuns(ctx context.Context, dueBefore time.Time) ([]domain.AutomationRun, error)
	AbandonStaleAutomationRuns(ctx context.Context, staleBefore time.Time) (int, error)
	CompleteAutomationRun(ctx context.Context, id string, generation int64, status string, finishedAt time.Time, errorCode, errorMessage *string) error
	ListAutomationRuns(ctx context.Context, filter AutomationRunFilter) ([]domain.AutomationRun, int, error)
	LatestAutomationRunByJob(ctx context.Context) (map[string]domain.AutomationRun, error)
	ListAutomationRunsByCycle(ctx context.Context, cycleKey string) ([]domain.AutomationRun, error)
}

// WarmupExecutor performs the synthetic per-account request (root wires the
// proxy warmup path; tests provide offline stubs).
type WarmupExecutor interface {
	ExecuteWarmupForGeneration(ctx context.Context, accountID string, generation int64, model string, reasoningEffort *string, prompt string) error
}

type AutomationsService struct {
	store    AutomationsStore
	accounts Accounts
	warmups  WarmupExecutor
	now      func() time.Time
}

func NewAutomationsService(store AutomationsStore, accounts Accounts, warmups WarmupExecutor, now func() time.Time) *AutomationsService {
	if now == nil {
		now = time.Now
	}
	return &AutomationsService{store: store, accounts: accounts, warmups: warmups, now: now}
}

type AutomationValidationError struct {
	Code    string
	Message string
}

func (e *AutomationValidationError) Error() string { return e.Message }

func validateAutomationSchedule(schedule domain.AutomationSchedule) (domain.AutomationSchedule, error) {
	if schedule.Type != "daily" {
		return schedule, &AutomationValidationError{Code: "invalid_schedule_type", Message: "Unsupported schedule type"}
	}
	if _, err := time.Parse("15:04", schedule.Time); err != nil {
		return schedule, &AutomationValidationError{Code: "invalid_schedule_time", Message: "Schedule time must be HH:MM"}
	}
	timezoneName := resolveAutomationTimezone(schedule.Timezone)
	if _, err := time.LoadLocation(timezoneName); err != nil {
		return schedule, &AutomationValidationError{Code: "invalid_schedule_timezone", Message: "Unsupported schedule timezone"}
	}
	schedule.Timezone = timezoneName
	if schedule.ThresholdMinutes < 0 || schedule.ThresholdMinutes > automationMaxThresholdMinutes {
		return schedule, &AutomationValidationError{Code: "invalid_schedule_threshold", Message: "Schedule threshold must be between 0 and 240 minutes"}
	}
	if len(schedule.Days) == 0 {
		schedule.Days = append([]string(nil), automationWeekdays...)
	}
	allowed := map[string]bool{}
	var days []string
	for _, day := range schedule.Days {
		normalized := strings.ToLower(strings.TrimSpace(day))
		if !isAutomationWeekday(normalized) {
			return schedule, &AutomationValidationError{Code: "invalid_schedule_days", Message: "Unsupported schedule day"}
		}
		if allowed[normalized] {
			return schedule, &AutomationValidationError{Code: "invalid_schedule_days", Message: "Duplicate schedule days are not allowed"}
		}
		allowed[normalized] = true
		days = append(days, normalized)
	}
	schedule.Days = days
	return schedule, nil
}

func isAutomationWeekday(day string) bool {
	for _, candidate := range automationWeekdays {
		if candidate == day {
			return true
		}
	}
	return false
}

func resolveAutomationTimezone(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" || normalized == "server_default" || normalized == "server default" || normalized == "default" {
		name := time.Local.String()
		if name == "Local" {
			return "UTC"
		}
		return name
	}
	return strings.TrimSpace(value)
}

func normalizeReasoningEffort(value *string) (*string, error) {
	if value == nil || *value == "" {
		return nil, nil
	}
	normalized := strings.ToLower(strings.TrimSpace(*value))
	if !automationReasoningEfforts[normalized] {
		return nil, &AutomationValidationError{Code: "invalid_reasoning_effort", Message: "Unsupported reasoning effort"}
	}
	return &normalized, nil
}

func normalizeAutomationAccountIDs(values []string) ([]string, error) {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if seen[trimmed] {
			return nil, &AutomationValidationError{Code: "invalid_account_ids", Message: "Duplicate account IDs are not allowed"}
		}
		seen[trimmed] = true
		result = append(result, trimmed)
	}
	return result, nil
}

func (s *AutomationsService) CreateJob(ctx context.Context, job domain.AutomationJob) (domain.AutomationJob, error) {
	if strings.TrimSpace(job.Name) == "" || len(job.Name) > automationMaxNameLength {
		return job, &AutomationValidationError{Code: "invalid_name", Message: "Automation name must be 1-200 characters"}
	}
	schedule, err := validateAutomationSchedule(job.Schedule)
	if err != nil {
		return job, err
	}
	job.Schedule = schedule
	if strings.TrimSpace(job.Model) == "" {
		return job, &AutomationValidationError{Code: "invalid_model", Message: "Automation model is required"}
	}
	reasoning, err := normalizeReasoningEffort(job.ReasoningEffort)
	if err != nil {
		return job, err
	}
	job.ReasoningEffort = reasoning
	prompt := strings.TrimSpace(job.Prompt)
	if prompt == "" {
		prompt = domain.DefaultAutomationPrompt
	}
	if len(prompt) > automationMaxPromptLength {
		return job, &AutomationValidationError{Code: "invalid_prompt", Message: "Automation prompt must be at most 1000 characters"}
	}
	job.Prompt = prompt
	job.AccountIDs, err = normalizeAutomationAccountIDs(job.AccountIDs)
	if err != nil {
		return job, err
	}
	job.AccountScopeAll = len(job.AccountIDs) == 0
	job.ID = newAutomationID("auto_")
	now := s.now().UTC()
	job.CreatedAt, job.UpdatedAt = now, now
	if err := s.store.SaveAutomationJob(ctx, job); err != nil {
		return domain.AutomationJob{}, err
	}
	return s.store.GetAutomationJob(ctx, job.ID)
}

func (s *AutomationsService) UpdateJob(ctx context.Context, id string, patch domain.AutomationJob, patchSet map[string]bool) (domain.AutomationJob, error) {
	current, err := s.store.GetAutomationJob(ctx, id)
	if err != nil {
		return current, err
	}
	if patchSet["name"] {
		if strings.TrimSpace(patch.Name) == "" || len(patch.Name) > automationMaxNameLength {
			return current, &AutomationValidationError{Code: "invalid_name", Message: "Automation name must be 1-200 characters"}
		}
		current.Name = patch.Name
	}
	if patchSet["enabled"] {
		current.Enabled = patch.Enabled
	}
	if patchSet["includePausedAccounts"] {
		current.IncludePausedAccounts = patch.IncludePausedAccounts
	}
	if patchSet["schedule"] {
		schedule, err := validateAutomationSchedule(patch.Schedule)
		if err != nil {
			return current, err
		}
		current.Schedule = schedule
	}
	if patchSet["model"] {
		if strings.TrimSpace(patch.Model) == "" {
			return current, &AutomationValidationError{Code: "invalid_model", Message: "Automation model is required"}
		}
		current.Model = patch.Model
	}
	if patchSet["reasoningEffort"] {
		reasoning, err := normalizeReasoningEffort(patch.ReasoningEffort)
		if err != nil {
			return current, err
		}
		current.ReasoningEffort = reasoning
	}
	if patchSet["prompt"] {
		prompt := strings.TrimSpace(patch.Prompt)
		if prompt == "" {
			prompt = domain.DefaultAutomationPrompt
		}
		if len(prompt) > automationMaxPromptLength {
			return current, &AutomationValidationError{Code: "invalid_prompt", Message: "Automation prompt must be at most 1000 characters"}
		}
		current.Prompt = prompt
	}
	if patchSet["accountIds"] {
		accountIDs, err := normalizeAutomationAccountIDs(patch.AccountIDs)
		if err != nil {
			return current, err
		}
		current.AccountIDs = accountIDs
		current.AccountScopeAll = len(accountIDs) == 0
	}
	current.UpdatedAt = s.now().UTC()
	if err := s.store.SaveAutomationJob(ctx, current); err != nil {
		return domain.AutomationJob{}, err
	}
	return s.store.GetAutomationJob(ctx, id)
}

func (s *AutomationsService) DeleteJob(ctx context.Context, id string) error {
	return s.store.DeleteAutomationJob(ctx, id)
}

func (s *AutomationsService) automationEligible(account domain.Account, includePaused bool) bool {
	switch account.Status {
	case domain.AccountDeactivated, domain.AccountRateLimited, domain.AccountQuotaExceeded:
		return false
	case domain.AccountPaused:
		return includePaused
	}
	return !deletedAccount(account) && account.Kind == domain.AccountChatGPT
}

func (s *AutomationsService) dispatchAccounts(ctx context.Context, job domain.AutomationJob) ([]domain.Account, error) {
	accounts, err := s.accounts.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	targets := map[string]bool{}
	if !job.AccountScopeAll {
		for _, id := range job.AccountIDs {
			targets[id] = true
		}
	}
	result := make([]domain.Account, 0, len(accounts))
	for _, account := range accounts {
		if deletedAccount(account) {
			continue
		}
		if !job.AccountScopeAll && !targets[account.ID] {
			continue
		}
		if s.automationEligible(account, job.IncludePausedAccounts) {
			result = append(result, account)
		}
	}
	return result, nil
}

func automationSlotKey(jobID, cycleKey string, accountID *string) string {
	if accountID == nil {
		return cycleKey + ":none"
	}
	digest := sha1.Sum([]byte(jobID + ":" + cycleKey + ":" + *accountID))
	return "run_" + hex.EncodeToString(digest[:])[:20]
}

func newAutomationID(prefix string) string {
	entropy := make([]byte, 12)
	if _, err := rand.Read(entropy); err != nil {
		return prefix + fmt.Sprintf("%x", sha1.Sum([]byte(time.Now().String())))[:24]
	}
	return prefix + hex.EncodeToString(entropy)
}

func (s *AutomationsService) ensureCycleRuns(ctx context.Context, job domain.AutomationJob, cycleKey, trigger string, dueSlot time.Time) error {
	accounts, err := s.dispatchAccounts(ctx, job)
	if err != nil {
		return err
	}
	if len(accounts) == 0 {
		return s.store.EnsureAutomationRun(ctx, domain.AutomationRun{
			ID: newAutomationID("run_"), JobID: job.ID, CycleKey: cycleKey,
			SlotKey: automationSlotKey(job.ID, cycleKey, nil), Trigger: trigger,
			Model: job.Model, ReasoningEffort: job.ReasoningEffort, ScheduledFor: dueSlot,
		})
	}
	thresholdSeconds := job.Schedule.ThresholdMinutes * 60
	for index, account := range accounts {
		offset := 0
		if thresholdSeconds > 0 && len(accounts) > 1 {
			offset = thresholdSeconds * index / (len(accounts) - 1)
		}
		accountID := account.ID
		if err := s.store.EnsureAutomationRun(ctx, domain.AutomationRun{
			ID: newAutomationID("run_"), JobID: job.ID, CycleKey: cycleKey,
			SlotKey: automationSlotKey(job.ID, cycleKey, &accountID), Trigger: trigger,
			Model: job.Model, ReasoningEffort: job.ReasoningEffort,
			ScheduledFor: dueSlot.Add(time.Duration(offset) * time.Second), AccountID: &accountID,
			AccountGeneration: account.Generation,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *AutomationsService) executeClaimed(ctx context.Context, job domain.AutomationJob, run domain.AutomationRun) {
	now := s.now().UTC()
	finish := func(status, code, message string) {
		var codePtr, messagePtr *string
		if code != "" {
			codePtr = &code
		}
		if message != "" {
			messagePtr = &message
		}
		// The caller ctx may already be cancelled; the terminal write is durable.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = s.store.CompleteAutomationRun(cleanup, run.ID, run.AccountGeneration, status, now, codePtr, messagePtr)
	}
	if run.AccountID == nil {
		finish(domain.AutomationFailed, "no_available_accounts", "No available accounts configured for automation job")
		return
	}
	account, err := s.accounts.GetAccount(ctx, *run.AccountID)
	if err != nil || !s.automationEligible(account, job.IncludePausedAccounts) {
		finish(domain.AutomationFailed, "account_ineligible", "Account is not eligible for automation job")
		return
	}
	if account.Generation != run.AccountGeneration {
		finish(domain.AutomationFailed, "account_ineligible", "Account incarnation changed")
		return
	}
	if err := s.warmups.ExecuteWarmupForGeneration(ctx, *run.AccountID, run.AccountGeneration, job.Model, job.ReasoningEffort, job.Prompt); err != nil {
		// err.Error() can carry upstream credential echoes; persist fixed copy.
		finish(domain.AutomationFailed, "warmup_failed", "The warmup request failed")
		return
	}
	finish(domain.AutomationSuccess, "", "")
}

func (s *AutomationsService) executeDue(ctx context.Context, now time.Time) int {
	// Crash safety: expired running claims are abandoned, never replayed.
	_, _ = s.store.AbandonStaleAutomationRuns(ctx, now.Add(-automationStaleRunAge))
	claimed, err := s.store.ClaimDueAutomationRuns(ctx, now)
	if err != nil {
		return 0
	}
	jobs := map[string]domain.AutomationJob{}
	for _, run := range claimed {
		job, ok := jobs[run.JobID]
		if !ok {
			loaded, err := s.store.GetAutomationJob(ctx, run.JobID)
			if err != nil {
				continue
			}
			job = loaded
			jobs[run.JobID] = job
		}
		s.executeClaimed(ctx, job, run)
	}
	return len(claimed)
}

// RunDueJobs schedules the latest due cycle per enabled job and executes all
// due claims. It is idempotent and restart-safe: durable slot keys mean a
// second process can never duplicate a run.
func (s *AutomationsService) RunDueJobs(ctx context.Context) (int, error) {
	now := s.now().UTC()
	jobs, err := s.store.ListAutomationJobs(ctx)
	if err != nil {
		return 0, err
	}
	for _, job := range jobs {
		if !job.Enabled {
			continue
		}
		dueSlot := LatestDueSlotUTC(now, job.Schedule)
		// A slot older than the job's last update predates its current
		// configuration: creating it now would be an unplanned paid catch-up.
		if dueSlot.Before(job.UpdatedAt) {
			continue
		}
		cycleKey := fmt.Sprintf("scheduled:%s:%s", job.ID, dueSlot.Format(time.RFC3339))
		if err := s.ensureCycleRuns(ctx, job, cycleKey, domain.AutomationTriggerScheduled, dueSlot); err != nil {
			return 0, err
		}
	}
	return s.executeDue(ctx, now), nil
}

func (s *AutomationsService) RunNow(ctx context.Context, jobID string) (domain.AutomationRun, error) {
	job, err := s.store.GetAutomationJob(ctx, jobID)
	if err != nil {
		return domain.AutomationRun{}, err
	}
	now := s.now().UTC()
	cycleID := newAutomationID("")
	cycleKey := "manual:" + job.ID + ":" + cycleID
	if err := s.ensureCycleRuns(ctx, job, cycleKey, domain.AutomationTriggerManual, now); err != nil {
		return domain.AutomationRun{}, err
	}
	s.executeDue(ctx, now)
	runs, err := s.store.ListAutomationRunsByCycle(ctx, cycleKey)
	if err != nil || len(runs) == 0 {
		return domain.AutomationRun{}, err
	}
	rollup := CycleRollup(runs)
	rollup.ID = runs[0].ID
	rollup.JobID = job.ID
	rollup.Trigger = domain.AutomationTriggerManual
	rollup.Model = job.Model
	rollup.ReasoningEffort = job.ReasoningEffort
	return rollup, nil
}

// RunAutomationsPoller blocks until ctx is done; wire one goroutine from the
// composition root and join it before closing the store.
func (s *AutomationsService) RunAutomationsPoller(ctx context.Context) {
	ticker := time.NewTicker(automationPollerInterval)
	defer ticker.Stop()
	for {
		_, _ = s.RunDueJobs(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func LatestDueSlotUTC(now time.Time, schedule domain.AutomationSchedule) time.Time {
	location, _ := time.LoadLocation(resolveAutomationTimezone(schedule.Timezone))
	if location == nil {
		location = time.UTC
	}
	hour, minute := parseScheduleTime(schedule.Time)
	allowed := map[string]bool{}
	for _, day := range schedule.Days {
		allowed[strings.ToLower(day)] = true
	}
	local := now.In(location)
	candidate := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, location)
	if candidate.After(local) {
		candidate = candidate.AddDate(0, 0, -1)
	}
	for range 8 {
		if allowed[weekdayCode(candidate)] {
			return candidate.UTC()
		}
		candidate = candidate.AddDate(0, 0, -1)
	}
	return now.UTC()
}

func NextRunUTC(now time.Time, schedule domain.AutomationSchedule) time.Time {
	location, _ := time.LoadLocation(schedule.Timezone)
	if location == nil {
		location = time.UTC
	}
	hour, minute := parseScheduleTime(schedule.Time)
	allowed := map[string]bool{}
	for _, day := range schedule.Days {
		allowed[strings.ToLower(day)] = true
	}
	local := now.In(location)
	candidate := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, location)
	if !candidate.After(local) {
		candidate = candidate.AddDate(0, 0, 1)
	}
	for range 8 {
		if allowed[weekdayCode(candidate)] {
			return candidate.UTC()
		}
		candidate = candidate.AddDate(0, 0, 1)
	}
	return now.UTC()
}

func weekdayCode(value time.Time) string {
	return automationWeekdays[int(value.Weekday()+6)%7]
}

func parseScheduleTime(value string) (int, int) {
	parsed, err := time.Parse("15:04", value)
	if err != nil {
		return 0, 0
	}
	return parsed.Hour(), parsed.Minute()
}

func CycleRollup(runs []domain.AutomationRun) domain.AutomationRun {
	var representative domain.AutomationRun
	if len(runs) == 0 {
		return representative
	}
	representative = runs[0]
	statusCounts := map[string]int{}
	for _, run := range runs {
		statusCounts[run.Status]++
	}
	switch {
	case statusCounts[domain.AutomationRunning] > 0 || statusCounts[domain.AutomationPending] > 0:
		representative.Status = domain.AutomationRunning
	case statusCounts[domain.AutomationFailed] == len(runs):
		representative.Status = domain.AutomationFailed
	case statusCounts[domain.AutomationFailed] > 0:
		representative.Status = domain.AutomationPartial
	default:
		representative.Status = domain.AutomationSuccess
	}
	return representative
}
