package domain

import "time"

type ReportFilter struct {
	Start, End     time.Time
	PreviousStart  time.Time
	Location       *time.Location
	AccountIDs     []string
	APIKeyIDs      []string
	Model          string
	UserAgentGroup string
}

type ReportSummary struct {
	TotalCostUSD                float64 `json:"totalCostUsd"`
	TotalInputTokens            int64   `json:"totalInputTokens"`
	TotalOutputTokens           int64   `json:"totalOutputTokens"`
	TotalReasoningTokens        int64   `json:"totalReasoningTokens"`
	ReasoningUsageKnownRequests int64   `json:"reasoningUsageKnownRequests"`
	TotalCachedTokens           int64   `json:"totalCachedTokens"`
	TotalRequests               int64   `json:"totalRequests"`
	TotalCancelled              int64   `json:"totalCancelled"`
	TotalErrors                 int64   `json:"totalErrors"`
	TotalConversations          int64   `json:"totalConversations"`
	ActiveAccounts              int64   `json:"activeAccounts"`
	AvgCostPerDay               float64 `json:"avgCostPerDay"`
	AvgRequestsPerDay           float64 `json:"avgRequestsPerDay"`
}

type DailyReportRow struct {
	Date              string  `json:"date"`
	Requests          int64   `json:"requests"`
	Conversations     int64   `json:"conversations"`
	InputTokens       int64   `json:"inputTokens"`
	OutputTokens      int64   `json:"outputTokens"`
	ReasoningTokens   *int64  `json:"reasoningTokens"`
	CachedInputTokens int64   `json:"cachedInputTokens"`
	CostUSD           float64 `json:"costUsd"`
	ActiveAccounts    int64   `json:"activeAccounts"`
	CancelledCount    int64   `json:"cancelledCount"`
	ErrorCount        int64   `json:"errorCount"`
	MedianTTFTMS      float64 `json:"medianTtftMs"`
	MedianTPS         float64 `json:"medianTps"`
	MedianQueueMS     float64 `json:"medianQueueMs"`
}

type ModelCostEntry struct {
	Model      string  `json:"model"`
	CostUSD    float64 `json:"costUsd"`
	Requests   int64   `json:"requests"`
	Percentage float64 `json:"percentage"`
}

type UserAgentCostEntry struct {
	UserAgent  string  `json:"useragent"`
	CostUSD    float64 `json:"costUsd"`
	Requests   int64   `json:"requests"`
	Percentage float64 `json:"percentage"`
}

type AccountCostEntry struct {
	AccountID *string `json:"accountId"`
	Alias     *string `json:"alias"`
	CostUSD   float64 `json:"costUsd"`
	Requests  int64   `json:"requests"`
}

type ReportComparison struct {
	CanCompare bool `json:"canCompare"`
	Previous   struct {
		TotalCostUSD  float64 `json:"totalCostUsd"`
		TotalTokens   int64   `json:"totalTokens"`
		TotalRequests int64   `json:"totalRequests"`
	} `json:"previous"`
}

type ReportsResponse struct {
	Summary     ReportSummary        `json:"summary"`
	Comparison  ReportComparison     `json:"comparison"`
	Daily       []DailyReportRow     `json:"daily"`
	ByModel     []ModelCostEntry     `json:"byModel"`
	ByUserAgent []UserAgentCostEntry `json:"byUseragent"`
	ByAccount   []AccountCostEntry   `json:"byAccount"`
}

// KeyReportsResponse explicitly excludes upstream account identities and raw logs.
type KeyReportsResponse struct {
	KeyReportLimitSummary
	Summary     ReportSummary        `json:"summary"`
	Comparison  ReportComparison     `json:"comparison"`
	Daily       []DailyReportRow     `json:"daily"`
	ByModel     []ModelCostEntry     `json:"byModel"`
	ByUserAgent []UserAgentCostEntry `json:"byUseragent"`
}

type KeyReportLimitSummary struct {
	Limits []LimitRule     `json:"limits"`
	Group  *KeyReportGroup `json:"group"`
}

type KeyReportGroup struct {
	Name         string               `json:"name"`
	Keys         []KeyReportGroupKey  `json:"keys"`
	AccountQuota *KeyReportGroupQuota `json:"accountQuota"`
}

type KeyReportGroupQuota struct {
	AccountCount             int                         `json:"accountCount"`
	Windows                  []KeyReportGroupQuotaWindow `json:"windows"`
	PurchasedCredits         *float64                    `json:"purchasedCredits"`
	CreditsUnlimited         bool                        `json:"creditsUnlimited"`
	CreditsKnownAccountCount int                         `json:"creditsKnownAccountCount"`
}

type KeyReportGroupQuotaWindow struct {
	Window       string     `json:"window"`
	UsedPercent  float64    `json:"usedPercent"`
	AccountCount int        `json:"accountCount"`
	NextResetAt  *time.Time `json:"nextResetAt"`
}

// KeyReportGroupKey is the safe subset for same-group limit summaries, not an
// administrator APIKey view: no credentials, prefixes or account assignments.
type KeyReportGroupKey struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	IsActive  bool        `json:"isActive"`
	ExpiresAt *time.Time  `json:"expiresAt"`
	IsCurrent bool        `json:"isCurrent"`
	Limits    []LimitRule `json:"limits"`
}

type RequestLogFilter struct {
	Limit, Offset    int
	Search           string
	ConversationID   string
	Since, Until     *time.Time
	AccountIDs       []string
	APIKeyIDs        []string
	Statuses         []string
	Models           []string
	ReasoningEfforts []string
	ModelOptions     []string
}

type RequestLogEntry struct {
	RequestedAt         time.Time `json:"requestedAt"`
	AccountID           *string   `json:"accountId"`
	PlanType            *string   `json:"planType"`
	APIKeyName          *string   `json:"apiKeyName"`
	APIKeyID            *string   `json:"apiKeyId"`
	RequestID           string    `json:"requestId"`
	RequestKind         string    `json:"requestKind"`
	Model               string    `json:"model"`
	Source              *string   `json:"source"`
	ModelSourceID       *string   `json:"modelSourceId"`
	ModelSourceKind     *string   `json:"modelSourceKind"`
	Transport           *string   `json:"transport"`
	UserAgent           *string   `json:"useragent"`
	UserAgentGroup      *string   `json:"useragentGroup"`
	ClientIP            *string   `json:"clientIp"`
	ConversationID      *string   `json:"conversationId"`
	ServiceTier         *string   `json:"serviceTier"`
	Status              string    `json:"status"`
	ErrorCode           *string   `json:"errorCode"`
	ErrorMessage        *string   `json:"errorMessage"`
	Tokens              *int64    `json:"tokens"`
	InputTokens         *int64    `json:"inputTokens"`
	OutputTokens        *int64    `json:"outputTokens"`
	ReasoningTokens     *int64    `json:"reasoningTokens"`
	CachedInputTokens   *int64    `json:"cachedInputTokens"`
	ReasoningEffort     *string   `json:"reasoningEffort"`
	CostUSD             *float64  `json:"costUsd"`
	LatencyMS           *int64    `json:"latencyMs"`
	LatencyFirstTokenMS *int64    `json:"latencyFirstTokenMs"`
	LatencyQueueMS      *int64    `json:"latencyQueueMs"`
}

type RequestLogsResponse struct {
	Requests     []RequestLogEntry `json:"requests"`
	Total        int64             `json:"total"`
	HasMore      bool              `json:"hasMore"`
	Conversation *struct {
		RequestCount      int64   `json:"requestCount"`
		AggregatedCostUSD float64 `json:"aggregatedCostUsd"`
	} `json:"conversation"`
}

type RequestLogModelOption struct {
	Model           string  `json:"model"`
	ReasoningEffort *string `json:"reasoningEffort"`
}

type RequestLogAPIKeyOption struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	KeyPrefix *string `json:"keyPrefix"`
}

type RequestLogOptions struct {
	AccountIDs   []string                 `json:"accountIds"`
	ModelOptions []RequestLogModelOption  `json:"modelOptions"`
	APIKeys      []RequestLogAPIKeyOption `json:"apiKeys"`
	Statuses     []string                 `json:"statuses"`
}
