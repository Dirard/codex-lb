package domain

import "time"

// Account is public metadata. Credentials are stored separately and never enter JSON responses.
type Account struct {
	ID                     string        `json:"accountId"`
	Generation             int64         `json:"-"`
	RouteRevision          int64         `json:"-"`
	Kind                   AccountKind   `json:"kind"`
	Provider               string        `json:"provider"`
	BaseURL                string        `json:"baseUrl,omitempty"`
	ChatGPTAccountID       string        `json:"chatgptAccountId,omitempty"`
	ChatGPTUserID          string        `json:"chatgptUserId,omitempty"`
	CodexInstallationID    string        `json:"codexInstallationId,omitempty"`
	Email                  string        `json:"email"`
	Alias                  string        `json:"alias,omitempty"`
	DisplayName            string        `json:"displayName"`
	WorkspaceID            string        `json:"workspaceId,omitempty"`
	WorkspaceLabel         string        `json:"workspaceLabel,omitempty"`
	SeatType               string        `json:"seatType,omitempty"`
	PlanType               string        `json:"planType"`
	RoutingPolicy          string        `json:"routingPolicy"`
	Status                 AccountStatus `json:"status"`
	DeactivationReason     string        `json:"deactivationReason,omitempty"`
	RequiresEgressDecision bool          `json:"requiresEgressDecision"`
	SecurityWorkAuthorized bool          `json:"securityWorkAuthorized"`
	LimitWarmupEnabled     bool          `json:"limitWarmupEnabled"`
	CreatedAt              time.Time     `json:"createdAt"`
	LastRefresh            *time.Time    `json:"lastRefresh,omitempty"`
}

type AccountKind string

const (
	AccountChatGPT  AccountKind = "chatgpt"
	AccountExternal AccountKind = "external"
)

type AccountStatus string

const (
	AccountActive         AccountStatus = "active"
	AccountRateLimited    AccountStatus = "rate_limited"
	AccountQuotaExceeded  AccountStatus = "quota_exceeded"
	AccountPaused         AccountStatus = "paused"
	AccountReauthRequired AccountStatus = "reauth_required"
	AccountDeactivated    AccountStatus = "deactivated"
)

// AccountCredential holds ciphertext only. The vault owns encryption and decryption.
type AccountCredential struct {
	AccountID             string `json:"-"`
	Generation            int64  `json:"-"`
	RouteRevision         int64  `json:"-"`
	AccessTokenEncrypted  []byte `json:"-"`
	RefreshTokenEncrypted []byte `json:"-"`
	IDTokenEncrypted      []byte `json:"-"`
	ExternalKeyEncrypted  []byte `json:"-"`
}

type AccountQuota struct {
	AccountID     string     `json:"accountId"`
	Window        string     `json:"window"`
	UsedPercent   float64    `json:"usedPercent"`
	ResetAt       *time.Time `json:"resetAt"`
	WindowMinutes *int       `json:"windowMinutes"`
	ObservedAt    time.Time  `json:"observedAt"`
}

type LimitType string

const (
	LimitTotalTokens  LimitType = "total_tokens"
	LimitInputTokens  LimitType = "input_tokens"
	LimitOutputTokens LimitType = "output_tokens"
	LimitCostUSD      LimitType = "cost_usd"
	LimitCredits      LimitType = "credits"
)

type LimitWindow string

const (
	WindowDaily     LimitWindow = "daily"
	WindowWeekly    LimitWindow = "weekly"
	WindowMonthly   LimitWindow = "monthly"
	WindowFiveHours LimitWindow = "5h"
	WindowSevenDays LimitWindow = "7d"
)

// MaxValue is in tokens for token limits and microdollars for cost_usd.
type LimitRule struct {
	ID           int64       `json:"id,omitempty"`
	Type         LimitType   `json:"limitType"`
	Window       LimitWindow `json:"limitWindow"`
	MaxValue     int64       `json:"maxValue"`
	CurrentValue int64       `json:"currentValue"`
	ModelFilter  *string     `json:"modelFilter"`
	ResetAt      time.Time   `json:"resetAt,omitempty"`
}

type AccountGroup struct {
	ID         string      `json:"id"`
	Name       string      `json:"name"`
	AccountIDs []string    `json:"accountIds"`
	Limits     []LimitRule `json:"limits"`
	KeyCount   int         `json:"keyCount"`
	CreatedAt  time.Time   `json:"createdAt"`
}

type APIKey struct {
	ID                            string      `json:"id"`
	Name                          string      `json:"name"`
	KeyHash                       string      `json:"-"`
	KeyPrefix                     string      `json:"keyPrefix"`
	GroupID                       *string     `json:"groupId"`
	AllowedModels                 []string    `json:"allowedModels"`
	ApplyToCodexModel             bool        `json:"applyToCodexModel"`
	EnforcedModel                 *string     `json:"enforcedModel"`
	AllowedReasoningEfforts       []string    `json:"allowedReasoningEfforts"`
	EnforcedReasoningEffort       *string     `json:"enforcedReasoningEffort"`
	EnforcedServiceTier           *string     `json:"enforcedServiceTier"`
	TrafficClass                  string      `json:"trafficClass"`
	TransportPolicyOverride       *string     `json:"transportPolicyOverride"`
	UsageSections                 string      `json:"usageSections"`
	AccountAssignmentScopeEnabled bool        `json:"accountAssignmentScopeEnabled"`
	SourceAssignmentScopeEnabled  bool        `json:"sourceAssignmentScopeEnabled"`
	AssignedAccountIDs            []string    `json:"assignedAccountIds"`
	AssignedSourceIDs             []string    `json:"assignedSourceIds"`
	ExpiresAt                     *time.Time  `json:"expiresAt"`
	IsActive                      bool        `json:"isActive"`
	CreatedAt                     time.Time   `json:"createdAt"`
	LastUsedAt                    *time.Time  `json:"lastUsedAt"`
	Limits                        []LimitRule `json:"limits"`
}

type UsageAmount struct {
	InputTokens       int64 `json:"inputTokens"`
	OutputTokens      int64 `json:"outputTokens"`
	CachedInputTokens int64 `json:"cachedInputTokens"`
	ReasoningTokens   int64 `json:"reasoningTokens"`
	CostMicrodollars  int64 `json:"costMicrodollars"`
}

type UsageEvent struct {
	RequestID            string      `json:"requestId"`
	ReservationID        string      `json:"reservationId,omitempty"`
	APIKeyID             string      `json:"apiKeyId,omitempty"`
	AccountID            string      `json:"accountId,omitempty"`
	AccountGeneration    int64       `json:"-"`
	ModelSourceID        string      `json:"modelSourceId,omitempty"`
	Model                string      `json:"model"`
	ConversationID       string      `json:"conversationId,omitempty"`
	UserAgent            string      `json:"useragent,omitempty"`
	UserAgentGroup       string      `json:"useragentGroup,omitempty"`
	ClientIP             string      `json:"clientIp,omitempty"`
	ReasoningEffort      string      `json:"reasoningEffort,omitempty"`
	PlanType             string      `json:"planType,omitempty"`
	Source               string      `json:"source,omitempty"`
	Transport            string      `json:"transport,omitempty"`
	ServiceTier          string      `json:"serviceTier,omitempty"`
	RequestKind          string      `json:"requestKind"`
	Status               string      `json:"status"`
	ErrorCode            string      `json:"errorCode,omitempty"`
	RequestedAt          time.Time   `json:"requestedAt"`
	QueueLatencyMS       int64       `json:"queueLatencyMs"`
	ConnectLatencyMS     int64       `json:"connectLatencyMs"`
	FirstEventMS         int64       `json:"firstEventMs"`
	FirstTokenMS         int64       `json:"firstTokenMs"`
	TotalLatencyMS       int64       `json:"totalLatencyMs"`
	ReasoningTokensKnown bool        `json:"reasoningTokensKnown"`
	Usage                UsageAmount `json:"usage"`
}

type UsageTotals struct {
	RequestCount int64       `json:"requestCount"`
	FailedCount  int64       `json:"failedCount"`
	Usage        UsageAmount `json:"usage"`
}

type ReservationRequest struct {
	ID                string      `json:"-"`
	APIKeyID          string      `json:"-"`
	AccountID         string      `json:"-"`
	AccountGeneration int64       `json:"-"`
	RouteRevision     int64       `json:"-"`
	Continuation      bool        `json:"-"`
	Model             string      `json:"-"`
	Budget            UsageAmount `json:"-"`
	Now               time.Time   `json:"-"`
}

type Reservation struct {
	ID                  string    `json:"id"`
	APIKeyID            string    `json:"apiKeyId"`
	AccountID           string    `json:"accountId,omitempty"`
	AccountGeneration   int64     `json:"-"`
	RouteRevision       int64     `json:"-"`
	Model               string    `json:"model"`
	Status              string    `json:"status"`
	NeedsReconciliation bool      `json:"needsReconciliation"`
	CreatedAt           time.Time `json:"createdAt"`
	UpdatedAt           time.Time `json:"updatedAt"`
}

type UsageSettlement struct {
	Status string
	Event  UsageEvent
}

// RuntimeSettings contains settings selected for the new runtime.
type RuntimeSettings struct {
	CodexClientVersion                                         string            `json:"codexClientVersion"`
	OpenAICacheAffinityMaxAgeSeconds                           int               `json:"openaiCacheAffinityMaxAgeSeconds"`
	StickyReallocationPrimaryBudgetThresholdPct                float64           `json:"stickyReallocationPrimaryBudgetThresholdPct"`
	StickyReallocationSecondaryBudgetThresholdPct              float64           `json:"stickyReallocationSecondaryBudgetThresholdPct"`
	APIKeyAuthEnabled                                          bool              `json:"apiKeyAuthEnabled"`
	HideUpstreamQuotaFromKeys                                  bool              `json:"hideUpstreamQuotaFromApiKeys"`
	StickyThreadsEnabled                                       bool              `json:"stickyThreadsEnabled"`
	TOTPRequiredOnLogin                                        bool              `json:"totpRequiredOnLogin"`
	ProhibitFastMode                                           bool              `json:"prohibitFastMode"`
	PreferEarlierResetAccounts                                 bool              `json:"preferEarlierResetAccounts"`
	PreferEarlierResetWindow                                   string            `json:"preferEarlierResetWindow"`
	ShowResetCreditBadges                                      bool              `json:"showResetCreditBadges"`
	ShowResetCreditExpiryBadge                                 bool              `json:"showResetCreditExpiryBadge"`
	ImportWithoutOverwrite                                     bool              `json:"importWithoutOverwrite"`
	RoutingStrategy                                            string            `json:"routingStrategy"`
	RelativeAvailabilityPower                                  float64           `json:"relativeAvailabilityPower"`
	RelativeAvailabilityTopK                                   int               `json:"relativeAvailabilityTopK"`
	SingleAccountID                                            string            `json:"singleAccountId,omitempty"`
	UpstreamStreamTransport                                    string            `json:"upstreamStreamTransport"`
	HTTPTransportPolicy                                        string            `json:"httpDownstreamTransportPolicy"`
	ProxyAccountResponseCreateLimit                            int               `json:"proxyAccountResponseCreateLimit"`
	ProxyAccountResponseCreateLimitEnvironmentValue            int               `json:"proxyAccountResponseCreateLimitEnvironmentValue"`
	ProxyAccountResponseCreateLimitOverride                    *int              `json:"proxyAccountResponseCreateLimitOverride"`
	ProxyAccountStreamLimit                                    int               `json:"proxyAccountStreamLimit"`
	ProxyAccountStreamLimitEnvironmentValue                    int               `json:"proxyAccountStreamLimitEnvironmentValue"`
	ProxyAccountStreamLimitOverride                            *int              `json:"proxyAccountStreamLimitOverride"`
	ProxyAccountStreamRecoveryReserve                          int               `json:"proxyAccountStreamRecoveryReserve"`
	ProxyAccountStreamRecoveryReserveEnvironmentValue          int               `json:"proxyAccountStreamRecoveryReserveEnvironmentValue"`
	ProxyAccountStreamRecoveryReserveOverride                  *int              `json:"proxyAccountStreamRecoveryReserveOverride"`
	ProxyApiKeyFairShareCongestionThresholdPct                 int               `json:"proxyApiKeyFairShareCongestionThresholdPct"`
	ProxyApiKeyFairShareCongestionThresholdPctEnvironmentValue int               `json:"proxyApiKeyFairShareCongestionThresholdPctEnvironmentValue"`
	ProxyApiKeyFairShareCongestionThresholdPctOverride         *int              `json:"proxyApiKeyFairShareCongestionThresholdPctOverride"`
	DashboardSessionTTL                                        int               `json:"dashboardSessionTtlSeconds"`
	WeeklyPaceWorkingDays                                      string            `json:"weeklyPaceWorkingDays"`
	WeeklyPaceSmoothingMinutes                                 int               `json:"weeklyPaceSmoothingMinutes"`
	WarmupModel                                                string            `json:"warmupModel"`
	LimitWarmupEnabled                                         bool              `json:"limitWarmupEnabled"`
	LimitWarmupModel                                           string            `json:"limitWarmupModel"`
	LimitWarmupWindows                                         string            `json:"limitWarmupWindows"`         // primary|secondary|both
	LimitWarmupPrompt                                          string            `json:"limitWarmupPrompt"`          // "Say OK."
	LimitWarmupCooldownSeconds                                 int               `json:"limitWarmupCooldownSeconds"` // min 60
	LimitWarmupExhaustedPercent                                float64           `json:"limitWarmupExhaustedThresholdPercent"`
	LimitWarmupIdlePercent                                     float64           `json:"limitWarmupIdleThresholdPercent"`
	LimitWarmupMinAvailablePercent                             float64           `json:"limitWarmupMinAvailablePercent"`
	LimitWarmupStaggeredIdleEnabled                            bool              `json:"limitWarmupStaggeredIdleEnabled"`
	AdditionalQuotaRoutingPolicies                             map[string]string `json:"additionalQuotaRoutingPolicies"`
	RequestLogRetentionDays                                    *int              `json:"requestLogRetentionOverrideDays"`
	UsageHistoryRetentionDays                                  *int              `json:"usageHistoryRetentionOverrideDays"`
	Version                                                    int64             `json:"version"`
}

type AdminSecret struct {
	PasswordHash         string `json:"-"`
	TOTPSecretEncrypted  []byte `json:"-"`
	TOTPLastVerifiedStep *int64 `json:"-"`
}
