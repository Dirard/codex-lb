package application

import (
	"context"
	"strings"
	"time"

	"codex-lb/internal/domain"
)

type AutomationJobView struct {
	domain.AutomationJob
	NextRunAt *time.Time
	LastRun   *AutomationRunView
}

type AutomationRunView struct {
	domain.AutomationRun
	JobName           *string
	EffectiveStatus   *string
	TotalAccounts     *int
	CompletedAccounts *int
	PendingAccounts   *int
}

type AutomationPage[T any] struct {
	Items   []T
	Total   int
	HasMore bool
}

func (s *AutomationsService) runViews(ctx context.Context, runs []domain.AutomationRun) ([]AutomationRunView, error) {
	jobs := map[string]domain.AutomationJob{}
	for _, run := range runs {
		if _, ok := jobs[run.JobID]; !ok {
			job, err := s.store.GetAutomationJob(ctx, run.JobID)
			if err != nil {
				continue
			}
			jobs[run.JobID] = job
		}
	}
	views := make([]AutomationRunView, 0, len(runs))
	for _, run := range runs {
		view := AutomationRunView{AutomationRun: run}
		if job, ok := jobs[run.JobID]; ok {
			jobName := job.Name
			view.JobName = &jobName
		}
		cycleRuns, err := s.store.ListAutomationRunsByCycle(ctx, run.CycleKey)
		if err == nil && len(cycleRuns) > 0 {
			rollup := CycleRollup(cycleRuns)
			effective := rollup.Status
			view.EffectiveStatus = &effective
			view.TotalAccounts, view.CompletedAccounts, view.PendingAccounts = cycleCounts(cycleRuns)
		}
		views = append(views, view)
	}
	return views, nil
}

func cycleCounts(runs []domain.AutomationRun) (*int, *int, *int) {
	total, completed, pending := len(runs), 0, 0
	for _, run := range runs {
		switch run.Status {
		case domain.AutomationSuccess, domain.AutomationFailed:
			completed++
		case domain.AutomationPending, domain.AutomationRunning:
			pending++
		}
	}
	return &total, &completed, &pending
}

func (s *AutomationsService) jobView(ctx context.Context, job domain.AutomationJob, latest map[string]domain.AutomationRun) (AutomationJobView, error) {
	view := AutomationJobView{AutomationJob: job}
	if job.Enabled {
		next := NextRunUTC(s.now().UTC(), job.Schedule)
		view.NextRunAt = &next
	}
	if run, ok := latest[job.ID]; ok {
		runs, err := s.runViews(ctx, []domain.AutomationRun{run})
		if err == nil && len(runs) == 1 {
			view.LastRun = &runs[0]
		}
	}
	return view, nil
}

// JobViewFor builds the single-job view after create/update.
func (s *AutomationsService) JobViewFor(ctx context.Context, job domain.AutomationJob) (AutomationJobView, error) {
	latest, err := s.store.LatestAutomationRunByJob(ctx)
	if err != nil {
		return AutomationJobView{}, err
	}
	return s.jobView(ctx, job, latest)
}

// RunOptions derives run filter options from stored runs.
func (s *AutomationsService) RunOptions(ctx context.Context, filter AutomationRunFilter) (accountIDs, models, statuses, triggers []string, err error) {
	runs, _, err := s.store.ListAutomationRuns(ctx, AutomationRunFilter{Limit: 1000})
	if err != nil {
		return nil, nil, nil, nil, err
	}
	accountSet, modelSet, statusSet, triggerSet := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, run := range runs {
		if run.AccountID != nil {
			accountSet[*run.AccountID] = true
		}
		modelSet[run.Model] = true
		statusSet[run.Status] = true
		triggerSet[run.Trigger] = true
	}
	return setToSlice(accountSet), setToSlice(modelSet), setToSlice(statusSet), setToSlice(triggerSet), nil
}

func (s *AutomationsService) ListJobs(ctx context.Context, filter AutomationRunFilter) (AutomationPage[AutomationJobView], error) {
	jobs, err := s.store.ListAutomationJobs(ctx)
	if err != nil {
		return AutomationPage[AutomationJobView]{}, err
	}
	latest, err := s.store.LatestAutomationRunByJob(ctx)
	if err != nil {
		return AutomationPage[AutomationJobView]{}, err
	}
	var filtered []domain.AutomationJob
	for _, job := range jobs {
		if !automationJobMatches(job, filter) {
			continue
		}
		filtered = append(filtered, job)
	}
	total := len(filtered)
	offset := max(filter.Offset, 0)
	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 25
	}
	if offset > total {
		offset = total
	}
	end := min(offset+limit, total)
	items := make([]AutomationJobView, 0, max(end-offset, 0))
	for _, job := range filtered[offset:end] {
		view, err := s.jobView(ctx, job, latest)
		if err != nil {
			return AutomationPage[AutomationJobView]{}, err
		}
		items = append(items, view)
	}
	return AutomationPage[AutomationJobView]{Items: items, Total: total, HasMore: end < total}, nil
}

func automationJobMatches(job domain.AutomationJob, filter AutomationRunFilter) bool {
	if filter.Search != "" &&
		!strings.Contains(strings.ToLower(job.Name), strings.ToLower(filter.Search)) &&
		!strings.Contains(strings.ToLower(job.Model), strings.ToLower(filter.Search)) {
		return false
	}
	if len(filter.Models) > 0 && !containsAny(job.Model, filter.Models) {
		return false
	}
	if len(filter.Statuses) > 0 {
		status := "disabled"
		if job.Enabled {
			status = "enabled"
		}
		if !containsAny(status, filter.Statuses) {
			return false
		}
	}
	if len(filter.AccountIDs) > 0 && !overlaps(job.AccountIDs, filter.AccountIDs) && !job.AccountScopeAll {
		return false
	}
	return true
}

func containsAny(value string, options []string) bool {
	for _, option := range options {
		if strings.EqualFold(value, option) {
			return true
		}
	}
	return false
}

func overlaps(values, candidates []string) bool {
	set := map[string]bool{}
	for _, value := range candidates {
		set[value] = true
	}
	for _, value := range values {
		if set[value] {
			return true
		}
	}
	return false
}

func (s *AutomationsService) ListRuns(ctx context.Context, filter AutomationRunFilter) (AutomationPage[AutomationRunView], error) {
	runs, total, err := s.store.ListAutomationRuns(ctx, filter)
	if err != nil {
		return AutomationPage[AutomationRunView]{}, err
	}
	views, err := s.runViews(ctx, runs)
	if err != nil {
		return AutomationPage[AutomationRunView]{}, err
	}
	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 25
	}
	return AutomationPage[AutomationRunView]{
		Items: views, Total: total, HasMore: filter.Offset+len(views) < total && len(views) == limit,
	}, nil
}

func (s *AutomationsService) RunDetails(ctx context.Context, runID string) (AutomationRunView, []AutomationRunView, error) {
	runs, _, err := s.store.ListAutomationRuns(ctx, AutomationRunFilter{Limit: 1000})
	if err != nil {
		return AutomationRunView{}, nil, err
	}
	var target *domain.AutomationRun
	for index := range runs {
		if runs[index].ID == runID {
			target = &runs[index]
			break
		}
	}
	if target == nil {
		return AutomationRunView{}, nil, domain.ErrNotFound
	}
	cycleRuns, err := s.store.ListAutomationRunsByCycle(ctx, target.CycleKey)
	if err != nil {
		return AutomationRunView{}, nil, err
	}
	views, err := s.runViews(ctx, cycleRuns)
	if err != nil || len(views) == 0 {
		return AutomationRunView{}, nil, err
	}
	rollup := views[0]
	rollup.ID = target.ID
	return rollup, views, nil
}

func (s *AutomationsService) JobOptions(ctx context.Context) (accountIDs, models, statuses, scheduleTypes []string, err error) {
	jobs, err := s.store.ListAutomationJobs(ctx)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	accountSet, modelSet := map[string]bool{}, map[string]bool{}
	statusSet, typeSet := map[string]bool{}, map[string]bool{}
	for _, job := range jobs {
		modelSet[job.Model] = true
		typeSet[job.Schedule.Type] = true
		if job.Enabled {
			statusSet["enabled"] = true
		} else {
			statusSet["disabled"] = true
		}
		for _, accountID := range job.AccountIDs {
			accountSet[accountID] = true
		}
	}
	return setToSlice(accountSet), setToSlice(modelSet), setToSlice(statusSet), setToSlice(typeSet), nil
}

func setToSlice(set map[string]bool) []string {
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sortStrings(result)
	return result
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
