package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

// registerAutomationsRoutes registers the warmup/ping automation API. The
// composition root supplies the service; handlers stay behind requireAdmin.
func (s *Server) registerAutomationsRoutes(mux *http.ServeMux, automations *application.AutomationsService) {
	list := func(w http.ResponseWriter, r *http.Request) {
		page, err := automations.ListJobs(r.Context(), automationFilter(r))
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, jobsResponse(page, time.Now()))
	}
	mux.HandleFunc("GET /api/automations", list)
	mux.HandleFunc("GET /api/automations/", list)
	mux.HandleFunc("GET /api/automations/options", func(w http.ResponseWriter, r *http.Request) {
		accountIDs, models, statuses, scheduleTypes, err := automations.JobOptions(r.Context())
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, struct {
			AccountIDs    []string `json:"accountIds"`
			Models        []string `json:"models"`
			Statuses      []string `json:"statuses"`
			ScheduleTypes []string `json:"scheduleTypes"`
		}{accountIDs, models, statuses, scheduleTypes})
	})
	mux.HandleFunc("GET /api/automations/runs", func(w http.ResponseWriter, r *http.Request) {
		filter := automationFilter(r)
		filter.Triggers = queryValues(r, "trigger")
		filter.JobIDs = queryValues(r, "automationId")
		page, err := automations.ListRuns(r.Context(), filter)
		if err != nil {
			s.fail(w, err)
			return
		}
		items := make([]automationRunJSON, 0, len(page.Items))
		for _, run := range page.Items {
			items = append(items, automationRunJSONFrom(run))
		}
		writeJSON(w, http.StatusOK, struct {
			Items   []automationRunJSON `json:"items"`
			Total   int                 `json:"total"`
			HasMore bool                `json:"hasMore"`
		}{items, page.Total, page.HasMore})
	})
	mux.HandleFunc("GET /api/automations/runs/options", func(w http.ResponseWriter, r *http.Request) {
		accountIDs, models, statuses, triggers, err := automations.RunOptions(r.Context(), automationFilter(r))
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, struct {
			AccountIDs []string `json:"accountIds"`
			Models     []string `json:"models"`
			Statuses   []string `json:"statuses"`
			Triggers   []string `json:"triggers"`
		}{accountIDs, models, statuses, triggers})
	})
	mux.HandleFunc("GET /api/automations/runs/{runId}/details", func(w http.ResponseWriter, r *http.Request) {
		run, accounts, err := automations.RunDetails(r.Context(), r.PathValue("runId"))
		if err != nil {
			s.fail(w, err)
			return
		}
		accountItems := make([]automationRunAccountJSON, 0, len(accounts))
		for _, account := range accounts {
			accountItems = append(accountItems, automationRunAccountJSONFrom(account.AutomationRun))
		}
		total, completed, pending := len(accounts), 0, 0
		for _, account := range accounts {
			switch account.Status {
			case domain.AutomationSuccess, domain.AutomationFailed:
				completed++
			default:
				pending++
			}
		}
		writeJSON(w, http.StatusOK, struct {
			Run               automationRunJSON          `json:"run"`
			Accounts          []automationRunAccountJSON `json:"accounts"`
			TotalAccounts     int                        `json:"totalAccounts"`
			CompletedAccounts int                        `json:"completedAccounts"`
			PendingAccounts   int                        `json:"pendingAccounts"`
		}{automationRunJSONFrom(run), accountItems, total, completed, pending})
	})
	mux.HandleFunc("POST /api/automations", func(w http.ResponseWriter, r *http.Request) {
		payload, ok := decodeAutomationJobPayload(w, r)
		if !ok {
			return
		}
		job, err := automations.CreateJob(r.Context(), payload.job())
		if err != nil {
			s.writeAutomationError(w, err)
			return
		}
		view, err := automations.JobViewFor(r.Context(), job)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, automationJobJSONFrom(view, time.Now()))
	})
	mux.HandleFunc("PATCH /api/automations/{id}", func(w http.ResponseWriter, r *http.Request) {
		payload, ok := decodeAutomationJobPayload(w, r)
		if !ok {
			return
		}
		job, err := automations.UpdateJob(r.Context(), r.PathValue("id"), payload.job(), payload.sets)
		if err != nil {
			s.writeAutomationError(w, err)
			return
		}
		view, err := automations.JobViewFor(r.Context(), job)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, automationJobJSONFrom(view, time.Now()))
	})
	mux.HandleFunc("DELETE /api/automations/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := automations.DeleteJob(r.Context(), r.PathValue("id")); err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, struct {
			Status string `json:"status"`
		}{"deleted"})
	})
	mux.HandleFunc("POST /api/automations/{id}/run-now", func(w http.ResponseWriter, r *http.Request) {
		run, err := automations.RunNow(r.Context(), r.PathValue("id"))
		if err != nil {
			s.writeAutomationError(w, err)
			return
		}
		view, _, err := automations.RunDetails(r.Context(), run.ID)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, automationRunJSONFrom(view))
	})
}

func (s *Server) writeAutomationError(w http.ResponseWriter, err error) {
	var validation *application.AutomationValidationError
	if errors.As(err, &validation) {
		writeError(w, http.StatusBadRequest, validation.Code, validation.Message)
		return
	}
	s.fail(w, err)
}

func automationFilter(r *http.Request) application.AutomationRunFilter {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	return application.AutomationRunFilter{
		Search:     r.URL.Query().Get("search"),
		AccountIDs: queryValues(r, "accountId"),
		Models:     queryValues(r, "model"),
		Statuses:   queryValues(r, "status"),
		JobIDs:     queryValues(r, "automationId"),
		Limit:      limit,
		Offset:     offset,
	}
}

func queryValues(r *http.Request, name string) []string {
	values, ok := r.URL.Query()[name]
	if !ok {
		return nil
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

type automationJobPayload struct {
	Name                  *string `json:"name"`
	Enabled               *bool   `json:"enabled"`
	IncludePausedAccounts *bool   `json:"includePausedAccounts"`
	Schedule              *struct {
		Type             string   `json:"type"`
		Time             string   `json:"time"`
		Timezone         string   `json:"timezone"`
		ThresholdMinutes int      `json:"thresholdMinutes"`
		Days             []string `json:"days"`
	} `json:"schedule"`
	Model           *string  `json:"model"`
	ReasoningEffort *string  `json:"reasoningEffort"`
	Prompt          *string  `json:"prompt"`
	AccountIDs      []string `json:"accountIds"`
	sets            map[string]bool
}

func (p automationJobPayload) job() domain.AutomationJob {
	job := domain.AutomationJob{}
	if p.Name != nil {
		job.Name = *p.Name
	}
	if p.Enabled != nil {
		job.Enabled = *p.Enabled
	}
	if p.IncludePausedAccounts != nil {
		job.IncludePausedAccounts = *p.IncludePausedAccounts
	}
	if p.Schedule != nil {
		job.Schedule = domain.AutomationSchedule{
			Type: p.Schedule.Type, Time: p.Schedule.Time, Timezone: p.Schedule.Timezone,
			ThresholdMinutes: p.Schedule.ThresholdMinutes, Days: p.Schedule.Days,
		}
	} else {
		job.Schedule.Type = "daily"
	}
	if p.Model != nil {
		job.Model = *p.Model
	}
	job.ReasoningEffort = p.ReasoningEffort
	if p.Prompt != nil {
		job.Prompt = *p.Prompt
	}
	job.AccountIDs = p.AccountIDs
	return job
}

func decodeAutomationJobPayload(w http.ResponseWriter, r *http.Request) (automationJobPayload, bool) {
	var payload automationJobPayload
	var raw map[string]json.RawMessage
	if !decodeJSON(w, r, &raw) {
		return payload, false
	}
	payload.sets = make(map[string]bool, len(raw))
	for field := range raw {
		payload.sets[field] = true
	}
	encoded, err := json.Marshal(raw)
	if err != nil || json.Unmarshal(encoded, &payload) != nil {
		return payload, false
	}
	return payload, true
}

type automationScheduleJSON struct {
	Type             string   `json:"type"`
	Time             string   `json:"time"`
	Timezone         string   `json:"timezone"`
	ThresholdMinutes int      `json:"thresholdMinutes"`
	Days             []string `json:"days"`
}

type automationRunJSON struct {
	ID              string  `json:"id"`
	JobID           string  `json:"jobId"`
	JobName         *string `json:"jobName"`
	Model           *string `json:"model"`
	ReasoningEffort *string `json:"reasoningEffort"`
	Trigger         string  `json:"trigger"`
	Status          string  `json:"status"`
	ScheduledFor    string  `json:"scheduledFor"`
	StartedAt       string  `json:"startedAt"`
	FinishedAt      *string `json:"finishedAt"`
	AccountID       *string `json:"accountId"`
	ErrorCode       *string `json:"errorCode"`
	ErrorMessage    *string `json:"errorMessage"`
	AttemptCount    int     `json:"attemptCount"`
	EffectiveStatus *string `json:"effectiveStatus"`
	TotalAccounts   *int    `json:"totalAccounts"`
	CompletedCount  *int    `json:"completedAccounts"`
	PendingCount    *int    `json:"pendingAccounts"`
	CycleKey        *string `json:"cycleKey"`
}

func automationRunJSONFrom(run application.AutomationRunView) automationRunJSON {
	model := run.Model
	cycleKey := run.CycleKey
	startedAt := time.Time{}
	if run.StartedAt != nil {
		startedAt = *run.StartedAt
	}
	view := automationRunJSON{
		ID: run.ID, JobID: run.JobID, JobName: run.JobName, Model: &model,
		ReasoningEffort: run.ReasoningEffort, Trigger: run.Trigger, Status: run.Status,
		ScheduledFor: run.ScheduledFor.UTC().Format(time.RFC3339Nano),
		StartedAt:    startedAt.UTC().Format(time.RFC3339Nano),
		FinishedAt:   formatOptionalTimeRFC(run.FinishedAt), AccountID: run.AccountID,
		ErrorCode: run.ErrorCode, ErrorMessage: run.ErrorMessage, AttemptCount: run.AttemptCount,
		EffectiveStatus: run.EffectiveStatus, TotalAccounts: run.TotalAccounts,
		CompletedCount: run.CompletedAccounts, PendingCount: run.PendingAccounts, CycleKey: &cycleKey,
	}
	return view
}

type automationRunAccountJSON struct {
	AccountID    string  `json:"accountId"`
	Status       string  `json:"status"`
	RunID        *string `json:"runId"`
	ScheduledFor *string `json:"scheduledFor"`
	StartedAt    *string `json:"startedAt"`
	FinishedAt   *string `json:"finishedAt"`
	ErrorCode    *string `json:"errorCode"`
	ErrorMessage *string `json:"errorMessage"`
}

func automationRunAccountJSONFrom(run domain.AutomationRun) automationRunAccountJSON {
	runID := run.ID
	return automationRunAccountJSON{
		AccountID: automationDerefString(run.AccountID), Status: run.Status, RunID: &runID,
		ScheduledFor: formatOptionalTimeRFC(&run.ScheduledFor), StartedAt: formatOptionalTimeRFC(run.StartedAt),
		FinishedAt: formatOptionalTimeRFC(run.FinishedAt), ErrorCode: run.ErrorCode, ErrorMessage: run.ErrorMessage,
	}
}

type automationJobJSON struct {
	ID                    string                 `json:"id"`
	Name                  string                 `json:"name"`
	Enabled               bool                   `json:"enabled"`
	IncludePausedAccounts bool                   `json:"includePausedAccounts"`
	Schedule              automationScheduleJSON `json:"schedule"`
	Model                 string                 `json:"model"`
	ReasoningEffort       *string                `json:"reasoningEffort"`
	Prompt                string                 `json:"prompt"`
	AccountScopeAll       bool                   `json:"accountScopeAll"`
	AccountIDs            []string               `json:"accountIds"`
	NextRunAt             *string                `json:"nextRunAt"`
	LastRun               *automationRunJSON     `json:"lastRun"`
}

func automationJobJSONFrom(job application.AutomationJobView, now time.Time) automationJobJSON {
	accountIDs := job.AccountIDs
	if accountIDs == nil {
		accountIDs = []string{}
	}
	view := automationJobJSON{
		ID: job.ID, Name: job.Name, Enabled: job.Enabled, IncludePausedAccounts: job.IncludePausedAccounts,
		Schedule: automationScheduleJSON{
			Type: job.Schedule.Type, Time: job.Schedule.Time, Timezone: job.Schedule.Timezone,
			ThresholdMinutes: job.Schedule.ThresholdMinutes, Days: job.Schedule.Days,
		},
		Model: job.Model, ReasoningEffort: job.ReasoningEffort, Prompt: job.Prompt,
		AccountScopeAll: job.AccountScopeAll, AccountIDs: accountIDs,
		NextRunAt: formatOptionalTimeRFC(job.NextRunAt),
	}
	if job.LastRun != nil {
		run := automationRunJSONFrom(*job.LastRun)
		view.LastRun = &run
	}
	return view
}

func jobsResponse(page application.AutomationPage[application.AutomationJobView], now time.Time) struct {
	Items   []automationJobJSON `json:"items"`
	Total   int                 `json:"total"`
	HasMore bool                `json:"hasMore"`
} {
	items := make([]automationJobJSON, 0, len(page.Items))
	for _, job := range page.Items {
		items = append(items, automationJobJSONFrom(job, now))
	}
	return struct {
		Items   []automationJobJSON `json:"items"`
		Total   int                 `json:"total"`
		HasMore bool                `json:"hasMore"`
	}{items, page.Total, page.HasMore}
}

func formatOptionalTimeRFC(value *time.Time) *string {
	if value == nil || value.IsZero() {
		return nil
	}
	formatted := value.UTC().Format(time.RFC3339Nano)
	return &formatted
}

func automationDerefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
