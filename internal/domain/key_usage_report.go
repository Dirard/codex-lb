package domain

import (
	"errors"
	"math"
	"time"
)

// Bounds of one report for API-key users: at most KeyUsageMaxDays local days,
// and at most KeyUsageMaxKeyDays resolved keys times days, which bounds the
// per-key day sums a report holds in memory.
const (
	KeyUsageMaxDays    = 731
	KeyUsageMaxKeyDays = 250000
)

// ErrReportTooLarge refuses a selection whose resolved keys times days exceed
// KeyUsageMaxKeyDays.
var ErrReportTooLarge = errors.New("report selection too large")

// KeySelection is the scope of a report for API-key users (dashboard
// redesign use case 5): groups by their current member keys, explicit keys
// and, with Ungrouped, the keys without a group. An empty selection is every
// key. Unknown ids select nothing.
type KeySelection struct {
	GroupIDs  []string
	KeyIDs    []string
	Ungrouped bool
}

// Empty reports whether the selection names nothing and so means every key.
func (s KeySelection) Empty() bool {
	return len(s.GroupIDs) == 0 && len(s.KeyIDs) == 0 && !s.Ungrouped
}

// Resolve is the step "resolve groups to their current member keys and unite
// them with the explicit keys and the ungrouped set" over the current
// (non-deleted, non-internal) keys, keeping their order.
func (s KeySelection) Resolve(keys []OverviewKey) []OverviewKey {
	if s.Empty() {
		return keys
	}
	groups := make(map[string]bool, len(s.GroupIDs))
	for _, id := range s.GroupIDs {
		groups[id] = true
	}
	explicit := make(map[string]bool, len(s.KeyIDs))
	for _, id := range s.KeyIDs {
		explicit[id] = true
	}
	var resolved []OverviewKey
	for _, key := range keys {
		switch {
		case explicit[key.ID],
			key.GroupID == nil && s.Ungrouped,
			key.GroupID != nil && groups[*key.GroupID]:
			resolved = append(resolved, key)
		}
	}
	return resolved
}

// KeyUsageQuery bounds the usage reads of one report in UTC: DayStarts are
// the local midnights of the period's days, End the local midnight after the
// last day and [PreviousStart, DayStarts[0]) the previous period of the same
// number of local days. Now projects expired limit windows.
type KeyUsageQuery struct {
	Selection     KeySelection
	DayStarts     []time.Time
	End           time.Time
	PreviousStart time.Time
	Now           time.Time
	// SkipPrevious leaves Previous zero; the per-day export does not show it.
	SkipPrevious bool
}

// KeyUsageData is what one report reads in one analytics snapshot: every
// group, the resolved keys with their projected limits, per-day sums for each
// resolved key with usage (one entry per day of the query) and the previous
// period's sums over the resolved keys.
type KeyUsageData struct {
	Groups        []OverviewGroupRef
	Keys          []OverviewKey
	Days          map[string][]UsageSums
	Previous      UsageSums
	RollupsUsable bool
}

// UsageSums accumulates report measures; CostUSD is unrounded.
type UsageSums struct {
	Requests, InputTokens, OutputTokens, CachedInputTokens int64
	ReasoningTokens, ReasoningKnown, Errors, Cancelled     int64
	CostUSD                                                float64
}

func (u *UsageSums) Add(o UsageSums) {
	u.Requests += o.Requests
	u.InputTokens += o.InputTokens
	u.OutputTokens += o.OutputTokens
	u.CachedInputTokens += o.CachedInputTokens
	u.ReasoningTokens += o.ReasoningTokens
	u.ReasoningKnown += o.ReasoningKnown
	u.Errors += o.Errors
	u.Cancelled += o.Cancelled
	u.CostUSD += o.CostUSD
}

// Tokens is input plus output tokens, the token figure of every report.
func (u UsageSums) Tokens() int64 { return u.InputTokens + u.OutputTokens }

// ReasoningKnownTokens is the reasoning token count, or nil when no counted
// request reported one.
func (u UsageSums) ReasoningKnownTokens() *int64 {
	if u.ReasoningKnown <= 0 && u.ReasoningTokens <= 0 {
		return nil
	}
	value := u.ReasoningTokens
	return &value
}

// Measures is the response shape: cost rounded to six decimals, reasoning
// tokens null when unknown.
func (u UsageSums) Measures() UsageMeasures {
	return UsageMeasures{Requests: u.Requests, InputTokens: u.InputTokens, OutputTokens: u.OutputTokens,
		CachedInputTokens: u.CachedInputTokens, ReasoningTokens: u.ReasoningKnownTokens(),
		CostUSD: RoundCostUSD(u.CostUSD), Errors: u.Errors, Cancelled: u.Cancelled}
}

// RoundCostUSD rounds a report cost to six decimals (microdollars).
func RoundCostUSD(value float64) float64 {
	rounded := math.Round(value*1e6) / 1e6
	if rounded == 0 {
		return 0
	}
	return rounded
}

type UsageMeasures struct {
	Requests          int64   `json:"requests"`
	InputTokens       int64   `json:"inputTokens"`
	OutputTokens      int64   `json:"outputTokens"`
	CachedInputTokens int64   `json:"cachedInputTokens"`
	ReasoningTokens   *int64  `json:"reasoningTokens"`
	CostUSD           float64 `json:"costUsd"`
	Errors            int64   `json:"errors"`
	Cancelled         int64   `json:"cancelled"`
}

// KeyUsageReport is the response of GET /api/reports/keys.
type KeyUsageReport struct {
	GeneratedAt time.Time        `json:"generatedAt"`
	Period      ReportPeriod     `json:"period"`
	Selection   ReportSelection  `json:"selection"`
	Summary     UsageMeasures    `json:"summary"`
	Previous    *UsageMeasures   `json:"previous"`
	ActiveKeys  int              `json:"activeKeys"`
	SeriesBy    string           `json:"seriesBy"`
	Series      []ReportSeries   `json:"series"`
	Groups      []GroupReportRow `json:"groups"`
	Keys        []KeyReportRow   `json:"keys"`
	Daily       []ReportDay      `json:"daily"`
}

type ReportPeriod struct {
	Start         string `json:"start"`
	End           string `json:"end"`
	Timezone      string `json:"timezone"`
	PreviousStart string `json:"previousStart"`
	PreviousEnd   string `json:"previousEnd"`
	Days          int    `json:"days"`
}

type ReportSelection struct {
	GroupIDs         []string `json:"groupIds"`
	KeyIDs           []string `json:"keyIds"`
	Ungrouped        bool     `json:"ungrouped"`
	AllKeys          bool     `json:"allKeys"`
	ResolvedKeyCount int      `json:"resolvedKeyCount"`
}

// Series kinds of the daily chart.
const (
	SeriesKey       = "key"
	SeriesGroup     = "group"
	SeriesUngrouped = "ungrouped"
	SeriesOther     = "other"
)

// ReportSeries names one stacked series of the daily chart; ReportDay.Series
// holds its points in the same order.
type ReportSeries struct {
	ID   *string `json:"id"`
	Name string  `json:"name"`
	Kind string  `json:"kind"`
}

type GroupReportRow struct {
	ID         *string       `json:"id"`
	Name       string        `json:"name"`
	KeyCount   int           `json:"keyCount"`
	ActiveKeys int           `json:"activeKeys"`
	Measures   UsageMeasures `json:"measures"`
	Share      float64       `json:"share"`
}

type KeyReportRow struct {
	ID              string        `json:"id"`
	Name            string        `json:"name"`
	Prefix          string        `json:"prefix"`
	GroupID         *string       `json:"groupId"`
	GroupName       *string       `json:"groupName"`
	Status          string        `json:"status"`
	Measures        UsageMeasures `json:"measures"`
	Share           float64       `json:"share"`
	LastUsedAt      *time.Time    `json:"lastUsedAt"`
	MaxLimitPercent *float64      `json:"maxLimitPercent"`
}

type ReportDay struct {
	Date   string              `json:"date"`
	Total  UsageMeasures       `json:"total"`
	Series []ReportSeriesPoint `json:"series"`
}

type ReportSeriesPoint struct {
	Requests int64   `json:"requests"`
	Tokens   int64   `json:"tokens"`
	CostUSD  float64 `json:"costUsd"`
}

// KeyDailyRow is one row of the per-day export: a local day and a key with
// requests, attributed to the key's current group.
type KeyDailyRow struct {
	Date      string
	GroupID   *string
	GroupName string
	KeyID     string
	KeyName   string
	Usage     UsageSums
}
