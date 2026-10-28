package domain

import "time"

// ShellStatus is what the dashboard header and status bar show on every
// administrator page; page polls carry it so no page needs a second poll.
type ShellStatus struct {
	Ready                      bool       `json:"ready"`
	LastSyncAt                 *time.Time `json:"lastSyncAt"`
	UsageFoldedAt              *time.Time `json:"usageFoldedAt"`
	RollupsUsable              bool       `json:"rollupsUsable"`
	ReauthRequiredAccounts     int        `json:"reauthRequiredAccounts"`
	AvailableResetCredits      int        `json:"availableResetCredits"`
	ShowResetCreditBadges      bool       `json:"showResetCreditBadges"`
	APIKeyAuthEnabled          bool       `json:"apiKeyAuthEnabled"`
	RoutingStrategy            string     `json:"routingStrategy"`
	StickyThreadsEnabled       bool       `json:"stickyThreadsEnabled"`
	PreferEarlierResetAccounts bool       `json:"preferEarlierResetAccounts"`
	PreferEarlierResetWindow   string     `json:"preferEarlierResetWindow"`
}

// ShellSnapshot is the stored part of the shell status, read in one snapshot.
type ShellSnapshot struct {
	Accounts      []Account
	LastSyncAt    *time.Time
	UsageFoldedAt *time.Time
	RollupsUsable bool
}

// UsageRollupStatus is the fold time and usability of the usage rollups.
type UsageRollupStatus struct {
	FoldedAt *time.Time
	Usable   bool
}

// OverviewAccount is one non-deleted account with its current quotas, credit
// status, quota refusal time and current group memberships.
type OverviewAccount struct {
	Account   Account
	Quotas    []AccountQuota
	Credits   *AccountCreditStatus
	RefusalAt *time.Time
	GroupIDs  []string
}

type OverviewGroupRef struct {
	ID, Name string
}

// OverviewKey is one non-deleted, non-internal key with its active limits;
// expired limit windows are projected to zero with the advanced reset.
type OverviewKey struct {
	ID, Name, Prefix string
	GroupID          *string
	IsActive         bool
	ExpiresAt        *time.Time
	LastUsedAt       *time.Time
	CreatedAt        time.Time
	Limits           []LimitRule
}

// Status is active, disabled or expired.
func (k OverviewKey) Status(now time.Time) string {
	switch {
	case !k.IsActive:
		return "disabled"
	case k.ExpiresAt != nil && !k.ExpiresAt.After(now):
		return "expired"
	}
	return "active"
}

// OverviewEntities are the accounts, groups and keys of one read snapshot.
type OverviewEntities struct {
	Accounts []OverviewAccount
	Groups   []OverviewGroupRef
	Keys     []OverviewKey
}

// OverviewUsageQuery bounds the overview's usage reads in UTC: DayStarts are
// the local midnights of the 8 days ending today (today last), today ends at
// Now; the last hour is [LastHourStart, Now) and the trailing window the 24
// hours before it.
type OverviewUsageQuery struct {
	DayStarts         []time.Time
	Now               time.Time
	YesterdaySameTime time.Time
	LastHourStart     time.Time
	TrailingStart     time.Time
}

// UsageDay is the reported requests and estimated cost of one local day.
type UsageDay struct {
	Requests int64
	CostUSD  float64
}

// OverviewScopeUsage is the usage of the pool or of one key: one entry per
// day of the query (only today when days are unknown) and the error windows.
type OverviewScopeUsage struct {
	Days     []UsageDay
	LastHour ErrorWindow
	Trailing ErrorWindow
}

// OverviewUsage is the overview's usage snapshot. DaysKnown is false when the
// rollups were unusable and only today was read from raw events; then
// YesterdaySameTime is nil.
type OverviewUsage struct {
	RollupsUsable     bool
	DaysKnown         bool
	Pool              OverviewScopeUsage
	YesterdaySameTime *UsageDay
	Keys              map[string]OverviewScopeUsage
}

// OverviewResponse is GET /api/overview.
type OverviewResponse struct {
	GeneratedAt        time.Time      `json:"generatedAt"`
	Timezone           string         `json:"timezone"`
	Shell              ShellStatus    `json:"shell"`
	Pool               PoolSummary    `json:"pool"`
	Alerts             []Alert        `json:"alerts"`
	Groups             []GroupRow     `json:"groups"`
	AttentionKeys      []AttentionKey `json:"attentionKeys"`
	AttentionKeysTotal int            `json:"attentionKeysTotal"`
}

type PoolSummary struct {
	Accounts                  HealthCounts `json:"accounts"`
	ExternalSources           int          `json:"externalSources"`
	Primary                   WindowHealth `json:"primary"`
	Secondary                 WindowHealth `json:"secondary"`
	SpendTodayUSD             float64      `json:"spendTodayUsd"`
	SpendYesterdaySameTimeUSD *float64     `json:"spendYesterdaySameTimeUsd"`
	RequestsToday             int64        `json:"requestsToday"`
	ErrorRateLastHour         *float64     `json:"errorRateLastHour"`
	Spend7dUSD                *float64     `json:"spend7dUsd"`
	Trend7d                   []*float64   `json:"trend7d"`
}

type LimitFill struct {
	MaxPercent      *float64 `json:"maxPercent"`
	KeysAtOrAbove90 int      `json:"keysAtOrAbove90"`
	KeysBlocked     int      `json:"keysBlocked"`
}

type KeyCounts struct {
	Active int `json:"active"`
	Total  int `json:"total"`
}

type GroupRow struct {
	ID              *string          `json:"id"`
	Name            string           `json:"name"`
	Attention       string           `json:"attention"`
	Accounts        HealthCounts     `json:"accounts"`
	Primary         WindowHealth     `json:"primary"`
	Secondary       WindowHealth     `json:"secondary"`
	LimitFill       LimitFill        `json:"limitFill"`
	Keys            KeyCounts        `json:"keys"`
	SpendTodayUSD   float64          `json:"spendTodayUsd"`
	Spend7dUSD      *float64         `json:"spend7dUsd"`
	Trend7d         []*float64       `json:"trend7d"`
	ProblemAccounts []ProblemAccount `json:"problemAccounts"`
	TopKeys         []TopKey         `json:"topKeys"`
	// Score orders the rows: 2 critical, 1 warning, 0 none.
	Score int `json:"-"`
}

type ProblemAccount struct {
	ID                        string        `json:"id"`
	Label                     string        `json:"label"`
	Status                    AccountStatus `json:"status"`
	Health                    AccountHealth `json:"health"`
	PrimaryRemainingPercent   *float64      `json:"primaryRemainingPercent"`
	PrimaryResetAt            *time.Time    `json:"primaryResetAt"`
	SecondaryRemainingPercent *float64      `json:"secondaryRemainingPercent"`
	SecondaryResetAt          *time.Time    `json:"secondaryResetAt"`
	// Action is reauth, resume or open.
	Action string `json:"action"`
}

type TopKey struct {
	ID                string     `json:"id"`
	Name              string     `json:"name"`
	SpendTodayUSD     float64    `json:"spendTodayUsd"`
	Spend7dUSD        *float64   `json:"spend7dUsd"`
	MaxLimitPercent   *float64   `json:"maxLimitPercent"`
	ErrorRateLastHour *float64   `json:"errorRateLastHour"`
	LastUsedAt        *time.Time `json:"lastUsedAt"`
}

// ProblemAccountLess orders problem accounts by the action they need, then
// label and id.
func ProblemAccountLess(a, b ProblemAccount) bool {
	if healthRank(a.Health) != healthRank(b.Health) {
		return healthRank(a.Health) < healthRank(b.Health)
	}
	if a.Label != b.Label {
		return a.Label < b.Label
	}
	return a.ID < b.ID
}

// GroupRowLess is the ordering of use case 2.1: attention score, problem
// accounts, keys at or above 90 %, spend today, then name.
func GroupRowLess(a, b GroupRow) bool {
	if a.Score != b.Score {
		return a.Score > b.Score
	}
	if a.Accounts.Problems() != b.Accounts.Problems() {
		return a.Accounts.Problems() > b.Accounts.Problems()
	}
	if a.LimitFill.KeysAtOrAbove90 != b.LimitFill.KeysAtOrAbove90 {
		return a.LimitFill.KeysAtOrAbove90 > b.LimitFill.KeysAtOrAbove90
	}
	if a.SpendTodayUSD != b.SpendTodayUSD {
		return a.SpendTodayUSD > b.SpendTodayUSD
	}
	if a.Name != b.Name {
		return a.Name < b.Name
	}
	return a.ID != nil && (b.ID == nil || *a.ID < *b.ID)
}

// AttentionLabel is none, warning or critical for a score.
func AttentionLabel(score int) string {
	switch score {
	case 2:
		return "critical"
	case 1:
		return "warning"
	}
	return "none"
}

// Directory is GET /api/directory: every entity the global search and the
// pickers can name.
type Directory struct {
	Keys     []DirectoryKey     `json:"keys"`
	Groups   []DirectoryGroup   `json:"groups"`
	Accounts []DirectoryAccount `json:"accounts"`
}

type DirectoryKey struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Prefix  string  `json:"prefix"`
	GroupID *string `json:"groupId"`
	Status  string  `json:"status"`
}

type DirectoryGroup struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	AccountCount int    `json:"accountCount"`
	KeyCount     int    `json:"keyCount"`
}

type DirectoryAccount struct {
	ID     string        `json:"id"`
	Label  string        `json:"label"`
	Email  string        `json:"email"`
	Status AccountStatus `json:"status"`
	Kind   AccountKind   `json:"kind"`
}
