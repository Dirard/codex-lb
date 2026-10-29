package domain

import "time"

// GroupsSummaryDays is the 30-day spend window of the Groups page: the local
// days ending today (dashboard redesign use case 7).
const GroupsSummaryDays = 30

// GroupKeysLimit bounds the member keys a summary row lists by 7-day spend.
const GroupKeysLimit = 20

// LimitTemplate is one group_limits row: a rule copied into every member
// key's own limits.
type LimitTemplate struct {
	Type        LimitType   `json:"limitType"`
	Window      LimitWindow `json:"limitWindow"`
	ModelFilter *string     `json:"modelFilter"`
	MaxValue    int64       `json:"maxValue"`
}

// GroupsSummaryEntities is the overview's entity snapshot plus the limit
// templates of every group, read in the same snapshot.
type GroupsSummaryEntities struct {
	OverviewEntities
	Limits map[string][]LimitTemplate
}

// GroupsSummaryUsageQuery is the overview's usage query plus the start of the
// 30-day window: the span [SpanStart, Overview.DayStarts[0]) completes the
// overview's days to 30 local days.
type GroupsSummaryUsageQuery struct {
	Overview  OverviewUsageQuery
	SpanStart time.Time
}

// GroupsSummaryUsage is the overview's usage snapshot and, from the same
// analytics snapshot, the requests and cost of the span before its first day
// per key and for the pool. SpanKnown is false while the rollups are
// unusable: the span is then not read.
type GroupsSummaryUsage struct {
	Overview  OverviewUsage
	SpanKnown bool
	SpanPool  UsageDay
	SpanKeys  map[string]UsageDay
}

// GroupsSummary is GET /api/groups/summary.
type GroupsSummary struct {
	GeneratedAt time.Time         `json:"generatedAt"`
	Timezone    string            `json:"timezone"`
	Shell       ShellStatus       `json:"shell"`
	Pool        GroupSummaryRow   `json:"pool"`
	Groups      []GroupSummaryRow `json:"groups"`
	Accounts    []GroupAccount    `json:"accounts"`
}

// GroupSummaryRow is an overview group row with the 30-day spend, the limit
// templates, the member accounts and the member keys by 7-day spend.
type GroupSummaryRow struct {
	GroupRow
	Spend30dUSD *float64        `json:"spend30dUsd"`
	Limits      []LimitTemplate `json:"limits"`
	AccountIDs  []string        `json:"accountIds"`
	TopKeys7d   []GroupKey      `json:"topKeys7d"`
}

// GroupKey is one member key of a summary row.
type GroupKey struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Prefix          string     `json:"prefix"`
	Status          string     `json:"status"`
	SpendTodayUSD   float64    `json:"spendTodayUsd"`
	Spend7dUSD      *float64   `json:"spend7dUsd"`
	Spend30dUSD     *float64   `json:"spend30dUsd"`
	MaxLimitPercent *float64   `json:"maxLimitPercent"`
	LastUsedAt      *time.Time `json:"lastUsedAt"`
}

// GroupAccount is one non-deleted account for the member picker.
type GroupAccount struct {
	ID                        string        `json:"id"`
	Label                     string        `json:"label"`
	Email                     string        `json:"email"`
	Kind                      AccountKind   `json:"kind"`
	Status                    AccountStatus `json:"status"`
	Health                    AccountHealth `json:"health"`
	PrimaryRemainingPercent   *float64      `json:"primaryRemainingPercent"`
	SecondaryRemainingPercent *float64      `json:"secondaryRemainingPercent"`
}

// GroupKeyLess ranks member keys by 7-day spend, then spend today, name and id.
func GroupKeyLess(a, b GroupKey) bool {
	if sa, sb := valueOrZero(a.Spend7dUSD), valueOrZero(b.Spend7dUSD); sa != sb {
		return sa > sb
	}
	if a.SpendTodayUSD != b.SpendTodayUSD {
		return a.SpendTodayUSD > b.SpendTodayUSD
	}
	if a.Name != b.Name {
		return a.Name < b.Name
	}
	return a.ID < b.ID
}

func valueOrZero(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}
