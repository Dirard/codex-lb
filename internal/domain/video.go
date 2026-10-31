package domain

import "time"

// VideoAccountStatus is the state of one account of the video pool. The pool
// is separate from Account: its rows never reach proxy routing or usage reports.
type VideoAccountStatus string

const (
	VideoAccountActive         VideoAccountStatus = "active"
	VideoAccountPaused         VideoAccountStatus = "paused"
	VideoAccountReauthRequired VideoAccountStatus = "reauth_required"
	VideoAccountAccessDenied   VideoAccountStatus = "access_denied"
)

// VideoAccount holds ciphertext tokens only; they never enter JSON responses.
type VideoAccount struct {
	ID                    string             `json:"id"`
	XAIUserID             string             `json:"-"`
	Email                 string             `json:"email"`
	Label                 string             `json:"label"`
	Status                VideoAccountStatus `json:"status"`
	OIDCClientID          string             `json:"-"`
	AccessTokenEncrypted  []byte             `json:"-"`
	RefreshTokenEncrypted []byte             `json:"-"`
	AccessExpiresAt       time.Time          `json:"-"`
	TokenVersion          int64              `json:"-"`
	CooldownUntil         *time.Time         `json:"cooldownUntil"`
	LastRefusalCode       string             `json:"lastRefusalCode"`
	LastUsedAt            *time.Time         `json:"lastUsedAt"`
	CreatedAt             time.Time          `json:"createdAt"`
	UpdatedAt             time.Time          `json:"updatedAt"`
	// UsagePercent is the last value read of the account's subscription pool;
	// nil until the first successful read.
	UsagePercent   *float64   `json:"usagePercent"`
	UsagePeriodEnd *time.Time `json:"usagePeriodEnd"`
	UsageCheckedAt *time.Time `json:"usageCheckedAt"`
	UsageError     string     `json:"usageError"`
}

type VideoJobStatus string

const (
	VideoJobDispatching VideoJobStatus = "dispatching"
	VideoJobPending     VideoJobStatus = "pending"
	VideoJobDone        VideoJobStatus = "done"
	VideoJobFailed      VideoJobStatus = "failed"
	VideoJobExpired     VideoJobStatus = "expired"
	VideoJobRefused     VideoJobStatus = "refused"
	VideoJobRejected    VideoJobStatus = "rejected"
	VideoJobUnknown     VideoJobStatus = "unknown"
)

// VideoJob is one attempt to create a video on one account. A refused attempt
// keeps its own row, and the request moves on with a new one.
type VideoJob struct {
	ID                string
	APIKeyID          string
	AccountID         string
	UpstreamRequestID string
	Model             string
	Mode              string
	RequestedSeconds  int
	Status            VideoJobStatus
	VideoURL          string
	DurationSeconds   *float64
	CostMicrodollars  *int64
	ErrorCode         string
	ErrorMessage      string
	NextPollAt        *time.Time
	CreatedAt         time.Time
	FinishedAt        *time.Time
}

// VideoJobFinish is the single allowed exit from an unfinished job.
type VideoJobFinish struct {
	Status           VideoJobStatus
	VideoURL         string
	DurationSeconds  *float64
	CostMicrodollars *int64
	ErrorCode        string
	ErrorMessage     string
	At               time.Time
}

// VideoAccountStats are per-account totals for the dashboard.
type VideoAccountStats struct {
	Unfinished       int64 `json:"unfinishedJobs"`
	Done             int64 `json:"doneJobs"`
	CostMicrodollars int64 `json:"-"`
}

// VideoKeyAccess is an API key as seen by the video allow list.
type VideoKeyAccess struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	KeyPrefix string `json:"keyPrefix"`
	IsActive  bool   `json:"isActive"`
	Allowed   bool   `json:"allowed"`
}

// VideoJobEntry is a job on the dashboard: it never carries the video URL.
type VideoJobEntry struct {
	ID               string
	KeyName          string
	AccountLabel     string
	Model            string
	Mode             string
	RequestedSeconds int
	Status           VideoJobStatus
	DurationSeconds  *float64
	CostMicrodollars *int64
	ErrorCode        string
	ErrorMessage     string
	CreatedAt        time.Time
	FinishedAt       *time.Time
}

// VideoPeriodTotals sums finished work since a moment.
type VideoPeriodTotals struct {
	Jobs             int64
	Done             int64
	Seconds          float64
	CostMicrodollars int64
}

// VideoJobCursor is the position of the last row of a job page.
type VideoJobCursor struct {
	CreatedAt time.Time
	ID        string
}
