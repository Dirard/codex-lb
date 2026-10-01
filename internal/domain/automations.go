package domain

import "time"

// Automation statuses: pending/running/success/failed are per-account run
// states; partial is a cycle rollup only.
const (
	AutomationPending = "pending"
	AutomationRunning = "running"
	AutomationSuccess = "success"
	AutomationFailed  = "failed"
	AutomationPartial = "partial"
)

const (
	AutomationTriggerScheduled = "scheduled"
	AutomationTriggerManual    = "manual"
)

const DefaultAutomationPrompt = "ping"

type AutomationSchedule struct {
	Type             string
	Time             string // HH:MM local
	Timezone         string
	ThresholdMinutes int
	Days             []string // mon..sun
}

type AutomationJob struct {
	ID                    string
	Name                  string
	Enabled               bool
	IncludePausedAccounts bool
	AccountScopeAll       bool
	Schedule              AutomationSchedule
	Model                 string
	ReasoningEffort       *string
	Prompt                string
	AccountIDs            []string
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type AutomationRun struct {
	ID                string
	JobID             string
	SlotKey           string // unique per (job, slot, account): restart-safe claim
	CycleKey          string
	Trigger           string
	Status            string
	Model             string
	ReasoningEffort   *string
	ScheduledFor      time.Time
	StartedAt         *time.Time
	FinishedAt        *time.Time
	AccountID         *string
	AccountGeneration int64 `json:"-"`
	AttemptCount      int
	ErrorCode         *string
	ErrorMessage      *string
}
