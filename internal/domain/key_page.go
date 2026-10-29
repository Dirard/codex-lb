package domain

import "time"

// Key page scopes: the administrator sees everything, the key owner a
// redacted view of their own key (dashboard redesign use cases 4 and 4.1).
const (
	KeyPageAdmin = "admin"
	KeyPageOwner = "owner"
)

// KeyPagePaceWindow is the trailing window whose consumption projects limit
// exhaustion.
const KeyPagePaceWindow = 3 * time.Hour

// KeyPageRecentLimit is the first page of recent requests on the key page;
// KeyRequestsMaxLimit bounds one recent-requests call and
// KeyErrorsScanLimit the rows one errors-only call examines (R23).
const (
	KeyPageRecentLimit  = 50
	KeyRequestsMaxLimit = 100
	KeyErrorsScanLimit  = 5000
)

// KeyPageTopModels is the number of models a day row names; the rest is
// summed into one "other" entry.
const KeyPageTopModels = 5

// KeyPageUsageQuery bounds the key page's analytics reads in UTC: the local
// days of the period start at DayStarts and the last one ends at End; the
// previous period of equal length is [PreviousStart, DayStarts[0]); the
// limit pace is read over [PaceStart, Now) for every model and for
// PaceModels; Recent is the first page of recent requests.
type KeyPageUsageQuery struct {
	KeyID         string
	DayStarts     []time.Time
	End           time.Time
	PreviousStart time.Time
	PaceStart     time.Time
	Now           time.Time
	PaceModels    []string
	Recent        KeyRequestsQuery
}

// KeyRequestsQuery is one page of a key's recent requests, newest first.
type KeyRequestsQuery struct {
	KeyID      string
	Before     *RequestLogCursor
	Limit      int
	ErrorsOnly bool
}

// KeyRequestsPage is a page of a key's recent requests: full request-log
// rows, projected per scope by the use case.
type KeyRequestsPage struct {
	Items      []RequestLogEntry
	NextCursor *string
}

// KeyUsageMeasures are the summed measures of a scope. CostUSD combines raw
// microdollars with folded legacy costs like every report.
type KeyUsageMeasures struct {
	Requests, Errors, Cancelled                  int64
	InputTokens, OutputTokens, CachedInputTokens int64
	ReasoningTokens, ReasoningKnown              int64
	CostUSD                                      float64
}

// KeyLatency holds the interpolated histogram quantiles of a scope in
// milliseconds (tokens per second for TPSP50); nil without samples.
type KeyLatency struct {
	TTFTP50, TTFTP95, TotalP50, TotalP95, QueueP95, TPSP50 *float64
}

// KeyModelAmount is one model's requests, tokens and cost.
type KeyModelAmount struct {
	Model    string
	Requests int64
	Tokens   int64
	CostUSD  float64
}

// KeyUsageDay is one local day of the period.
type KeyUsageDay struct {
	KeyUsageMeasures
	Latency KeyLatency
	Models  []KeyModelAmount
}

// KeyModelUsage is one model over the period.
type KeyModelUsage struct {
	Model string
	KeyUsageMeasures
	TTFTP50 *float64
}

// KeyUserAgentUsage is one user-agent group over the period.
type KeyUserAgentUsage struct {
	Group    string
	Requests int64
	CostUSD  float64
}

// KeyAccountUsage is one account over the period; AccountID is nil for
// requests without an account, Deleted marks discarded accounts' history.
type KeyAccountUsage struct {
	AccountID *string
	Deleted   bool
	Requests  int64
	CostUSD   float64
}

// KeyErrorCode is one error code over the period; LastSeenAt is the start of
// the latest UTC hour holding it.
type KeyErrorCode struct {
	Code       string
	Count      int64
	LastSeenAt time.Time
}

// KeyPaceAmounts is a key's consumption in limit units over the pace window.
type KeyPaceAmounts struct {
	InputTokens, OutputTokens, CostMicrodollars int64
}

// KeyLimitPace is the pace window's consumption for every model and per
// requested model.
type KeyLimitPace struct {
	All     KeyPaceAmounts
	ByModel map[string]KeyPaceAmounts
}

// KeyPageUsage is the key page's analytics snapshot. DetailsFrom is set when
// the period holds folded legacy hours, which carry no latency, user agents
// or error codes: those details are complete only from that instant.
type KeyPageUsage struct {
	RollupsUsable bool
	Days          []KeyUsageDay
	Total         KeyUsageMeasures
	TotalLatency  KeyLatency
	Previous      KeyUsageMeasures
	Models        []KeyModelUsage
	UserAgents    []KeyUserAgentUsage
	Accounts      []KeyAccountUsage
	ErrorCodes    []KeyErrorCode
	Pace          KeyLimitPace
	DetailsFrom   *time.Time
	Recent        KeyRequestsPage
}

// KeyPageResponse is GET /api/api-keys/{id}/page and GET
// /api/key-reports/page. Fields marked administrator-only are absent from the
// owner's JSON (R26).
type KeyPageResponse struct {
	Scope          string               `json:"scope"`
	GeneratedAt    time.Time            `json:"generatedAt"`
	Shell          *ShellStatus         `json:"shell,omitempty"` // administrator only
	Key            KeyPageKey           `json:"key"`
	Period         KeyPagePeriod        `json:"period"`
	RollupsUsable  bool                 `json:"rollupsUsable"`
	DetailsFrom    *time.Time           `json:"detailsFrom"`
	Limits         []KeyLimitState      `json:"limits"`
	Pool           *KeyPagePool         `json:"pool"`
	Group          *KeyPageGroup        `json:"group"`
	Summary        KeyPeriodSummary     `json:"summary"`
	Daily          []KeyDay             `json:"daily"`
	ByModel        []KeyModelRow        `json:"byModel"`
	ErrorsByCode   []KeyErrorCodeRow    `json:"errorsByCode"`
	UserAgents     []KeyUserAgentRow    `json:"userAgents"`
	ByAccount      *[]KeyAccountCostRow `json:"byAccount,omitempty"` // administrator only
	Lifetime       *UsageTotals         `json:"lifetime,omitempty"`  // administrator only
	RecentRequests []KeyRequestRow      `json:"recentRequests"`
	NextCursor     *string              `json:"nextCursor"`
}

// KeyPagePeriod is the validated period: inclusive local days.
type KeyPagePeriod struct {
	Start         string `json:"start"`
	End           string `json:"end"`
	Timezone      string `json:"timezone"`
	Days          int    `json:"days"`
	PreviousStart string `json:"previousStart"`
	PreviousEnd   string `json:"previousEnd"`
	IncludesToday bool   `json:"includesToday"`
}

// KeyPageKey are the key's properties. AssignedAccounts is administrator
// only; the owner sees only the counts.
type KeyPageKey struct {
	ID                            string                `json:"id"`
	Name                          string                `json:"name"`
	Prefix                        string                `json:"prefix"`
	Status                        string                `json:"status"`
	GroupID                       *string               `json:"groupId"`
	GroupName                     *string               `json:"groupName"`
	CreatedAt                     time.Time             `json:"createdAt"`
	LastUsedAt                    *time.Time            `json:"lastUsedAt"`
	ExpiresAt                     *time.Time            `json:"expiresAt"`
	AllowedModels                 []string              `json:"allowedModels"`
	EnforcedModel                 *string               `json:"enforcedModel"`
	EnforcedReasoningEffort       *string               `json:"enforcedReasoningEffort"`
	AllowedReasoningEfforts       []string              `json:"allowedReasoningEfforts"`
	TrafficClass                  string                `json:"trafficClass"`
	ServiceTier                   *string               `json:"serviceTier"`
	UsageSections                 []string              `json:"usageSections"`
	AccountAssignmentScopeEnabled bool                  `json:"accountAssignmentScopeEnabled"`
	SourceAssignmentScopeEnabled  bool                  `json:"sourceAssignmentScopeEnabled"`
	AssignedAccounts              *[]KeyAssignedAccount `json:"assignedAccounts,omitempty"` // administrator only
	AssignedAccountCount          int                   `json:"assignedAccountCount"`
	AssignedSourceCount           int                   `json:"assignedSourceCount"`
}

// KeyAssignedAccount is an assigned account's id and display label.
type KeyAssignedAccount struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// KeyLimitState is one active limit with its expired window projected to
// zero, its pace over the last 3 hours in limit units per hour and the
// projected exhaustion when that pace reaches the maximum before the reset.
// Cost limits are in microdollars.
type KeyLimitState struct {
	Type                  LimitType   `json:"type"`
	Window                LimitWindow `json:"window"`
	ModelFilter           *string     `json:"modelFilter"`
	Current               int64       `json:"current"`
	Max                   int64       `json:"max"`
	Percent               float64     `json:"percent"`
	ResetAt               time.Time   `json:"resetAt"`
	Expired               bool        `json:"expired"`
	PacePerHour           *float64    `json:"pacePerHour"`
	ProjectedExhaustionAt *time.Time  `json:"projectedExhaustionAt"`
	PaceBasis             string      `json:"paceBasis"`
}

// KeyPagePool is the pool (ungrouped key) or group (grouped key) remaining.
type KeyPagePool struct {
	Scope            string               `json:"scope"`
	AccountCount     int                  `json:"accountCount"`
	Primary          WindowHealth         `json:"primary"`
	Secondary        WindowHealth         `json:"secondary"`
	PurchasedCredits *KeyPurchasedCredits `json:"purchasedCredits"`
}

// KeyPurchasedCredits sums the scope's known purchased-credit balances like
// the key report's group summary: unknown balances are not zero.
type KeyPurchasedCredits struct {
	Remaining         *float64 `json:"remaining"`
	Unlimited         bool     `json:"unlimited"`
	KnownAccountCount int      `json:"knownAccountCount"`
}

// KeyPageGroup is the key's current group with the safe same-group
// mini-list of the key report.
type KeyPageGroup struct {
	ID   string              `json:"id"`
	Name string              `json:"name"`
	Keys []KeyReportGroupKey `json:"keys"`
}

// KeyPeriodSummary are the period KPIs; Previous is nil when the previous
// period has no requests.
type KeyPeriodSummary struct {
	Requests          int64              `json:"requests"`
	InputTokens       int64              `json:"inputTokens"`
	OutputTokens      int64              `json:"outputTokens"`
	CachedInputTokens int64              `json:"cachedInputTokens"`
	ReasoningTokens   *int64             `json:"reasoningTokens"`
	CacheShare        *float64           `json:"cacheShare"`
	CostUSD           float64            `json:"costUsd"`
	Errors            int64              `json:"errors"`
	ErrorRate         *float64           `json:"errorRate"`
	Cancelled         int64              `json:"cancelled"`
	TTFTP50MS         *float64           `json:"ttftP50Ms"`
	TTFTP95MS         *float64           `json:"ttftP95Ms"`
	TotalP50MS        *float64           `json:"totalP50Ms"`
	TotalP95MS        *float64           `json:"totalP95Ms"`
	QueueP95MS        *float64           `json:"queueP95Ms"`
	TPSP50            *float64           `json:"tpsP50"`
	Previous          *KeyPeriodPrevious `json:"previous"`
}

// KeyPeriodPrevious is the previous period of equal length.
type KeyPeriodPrevious struct {
	Requests int64   `json:"requests"`
	Tokens   int64   `json:"tokens"`
	CostUSD  float64 `json:"costUsd"`
}

// KeyDay is one local day; ByModel names the period's top models and sums
// the rest into one entry with Other set.
type KeyDay struct {
	Date              string        `json:"date"`
	Requests          int64         `json:"requests"`
	InputTokens       int64         `json:"inputTokens"`
	OutputTokens      int64         `json:"outputTokens"`
	CachedInputTokens int64         `json:"cachedInputTokens"`
	CostUSD           float64       `json:"costUsd"`
	Errors            int64         `json:"errors"`
	Cancelled         int64         `json:"cancelled"`
	ByModel           []KeyDayModel `json:"byModel"`
	TTFTP50MS         *float64      `json:"ttftP50Ms"`
	TTFTP95MS         *float64      `json:"ttftP95Ms"`
	TotalP50MS        *float64      `json:"totalP50Ms"`
	TotalP95MS        *float64      `json:"totalP95Ms"`
	QueueP95MS        *float64      `json:"queueP95Ms"`
	TPSP50            *float64      `json:"tpsP50"`
}

// KeyDayModel is one model's share of a day.
type KeyDayModel struct {
	Model    string  `json:"model"`
	Other    bool    `json:"other"`
	Requests int64   `json:"requests"`
	Tokens   int64   `json:"tokens"`
	CostUSD  float64 `json:"costUsd"`
}

// KeyModelRow is one model over the period; Share is the percent of the
// period's cost, or of its requests when nothing cost anything.
type KeyModelRow struct {
	Model             string   `json:"model"`
	Requests          int64    `json:"requests"`
	InputTokens       int64    `json:"inputTokens"`
	OutputTokens      int64    `json:"outputTokens"`
	CachedInputTokens int64    `json:"cachedInputTokens"`
	CostUSD           float64  `json:"costUsd"`
	Errors            int64    `json:"errors"`
	Share             float64  `json:"share"`
	TTFTP50MS         *float64 `json:"ttftP50Ms"`
}

// KeyErrorCodeRow is one error code; Code is empty for errors stored without
// a code (and for folded legacy hours), which have no LastSeenAt.
type KeyErrorCodeRow struct {
	Code       string     `json:"code"`
	Count      int64      `json:"count"`
	Share      float64    `json:"share"`
	LastSeenAt *time.Time `json:"lastSeenAt"`
}

// KeyUserAgentRow is one user-agent group; Share is the percent of requests.
type KeyUserAgentRow struct {
	Group    string  `json:"group"`
	Requests int64   `json:"requests"`
	CostUSD  float64 `json:"costUsd"`
	Share    float64 `json:"share"`
}

// KeyAccountCostRow is one account's cost of the period (administrator).
type KeyAccountCostRow struct {
	AccountID *string `json:"accountId"`
	Label     *string `json:"label"`
	Deleted   bool    `json:"deleted"`
	Requests  int64   `json:"requests"`
	CostUSD   float64 `json:"costUsd"`
	Share     float64 `json:"share"`
}

// KeyRequestRow is one recent request of the key. The administrator-only
// fields are absent from the owner's JSON (R26).
type KeyRequestRow struct {
	RequestedAt         time.Time `json:"requestedAt"`
	RequestID           string    `json:"requestId"`
	RequestKind         string    `json:"requestKind"`
	Model               string    `json:"model"`
	ReasoningEffort     *string   `json:"reasoningEffort"`
	Status              string    `json:"status"`
	ErrorCode           *string   `json:"errorCode"`
	InputTokens         *int64    `json:"inputTokens"`
	OutputTokens        *int64    `json:"outputTokens"`
	CachedInputTokens   *int64    `json:"cachedInputTokens"`
	ReasoningTokens     *int64    `json:"reasoningTokens"`
	CostUSD             *float64  `json:"costUsd"`
	LatencyMS           *int64    `json:"latencyMs"`
	LatencyFirstTokenMS *int64    `json:"latencyFirstTokenMs"`
	LatencyQueueMS      *int64    `json:"latencyQueueMs"`
	Transport           *string   `json:"transport"`
	ServiceTier         *string   `json:"serviceTier"`
	UserAgentGroup      *string   `json:"useragentGroup"`
	// Administrator only.
	AccountID       *string `json:"accountId,omitempty"`
	PlanType        *string `json:"planType,omitempty"`
	ClientIP        *string `json:"clientIp,omitempty"`
	ConversationID  *string `json:"conversationId,omitempty"`
	UserAgent       *string `json:"useragent,omitempty"`
	ErrorMessage    *string `json:"errorMessage,omitempty"`
	Source          *string `json:"source,omitempty"`
	ModelSourceID   *string `json:"modelSourceId,omitempty"`
	ModelSourceKind *string `json:"modelSourceKind,omitempty"`
}

// KeyRequestsResponse is GET /api/api-keys/{id}/requests and GET
// /api/key-reports/requests.
type KeyRequestsResponse struct {
	Items      []KeyRequestRow `json:"items"`
	NextCursor *string         `json:"nextCursor"`
}

// KeyRequestRowOf projects a request-log row for a scope: the owner never
// receives account identities, addresses, conversations, full user agents,
// messages or model-source ids.
func KeyRequestRowOf(entry RequestLogEntry, scope string) KeyRequestRow {
	row := KeyRequestRow{RequestedAt: entry.RequestedAt, RequestID: entry.RequestID, RequestKind: entry.RequestKind,
		Model: entry.Model, ReasoningEffort: entry.ReasoningEffort, Status: entry.Status, ErrorCode: entry.ErrorCode,
		InputTokens: entry.InputTokens, OutputTokens: entry.OutputTokens, CachedInputTokens: entry.CachedInputTokens,
		ReasoningTokens: entry.ReasoningTokens, CostUSD: entry.CostUSD, LatencyMS: entry.LatencyMS,
		LatencyFirstTokenMS: entry.LatencyFirstTokenMS, LatencyQueueMS: entry.LatencyQueueMS, Transport: entry.Transport,
		ServiceTier: entry.ServiceTier, UserAgentGroup: entry.UserAgentGroup}
	if scope == KeyPageAdmin {
		row.AccountID, row.PlanType, row.ClientIP = entry.AccountID, entry.PlanType, entry.ClientIP
		row.ConversationID, row.UserAgent, row.ErrorMessage = entry.ConversationID, entry.UserAgent, entry.ErrorMessage
		row.Source, row.ModelSourceID, row.ModelSourceKind = entry.Source, entry.ModelSourceID, entry.ModelSourceKind
	}
	return row
}

// RequestFailed reports a request status that counts as an error: anything
// but success, completed and cancelled, as every report counts it.
func RequestFailed(status string) bool {
	switch status {
	case "success", "completed", "cancelled":
		return false
	}
	return true
}
