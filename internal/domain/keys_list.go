package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// KeyListSort is a sort key of the keys list (dashboard redesign use case 6).
type KeyListSort string

const (
	KeySortName       KeyListSort = "name"
	KeySortCreated    KeyListSort = "created"
	KeySortSpendToday KeyListSort = "spend_today"
	KeySortSpend7d    KeyListSort = "spend_7d"
	KeySortSpend30d   KeyListSort = "spend_30d"
	KeySortLimitFill  KeyListSort = "limit_fill"
	KeySortLastUsed   KeyListSort = "last_used"
)

// Valid reports whether s is a known sort key.
func (s KeyListSort) Valid() bool {
	switch s {
	case KeySortName, KeySortCreated, KeySortSpendToday, KeySortSpend7d, KeySortSpend30d, KeySortLimitFill, KeySortLastUsed:
		return true
	}
	return false
}

// UsesSpend reports whether ordering by s needs the per-key spend.
func (s KeyListSort) UsesSpend() bool {
	return s == KeySortSpendToday || s == KeySortSpend7d || s == KeySortSpend30d
}

// Key statuses of the dashboard (see OverviewKey.Status).
const (
	KeyStatusActive   = "active"
	KeyStatusDisabled = "disabled"
	KeyStatusExpired  = "expired"
)

// KeyListFilter is the validated filter of the keys list; zero values filter
// nothing. GroupID and Ungrouped are exclusive.
type KeyListFilter struct {
	Query        string
	GroupID      *string
	Ungrouped    bool
	Status       string
	NearLimit    bool
	ExpiringDays int
	InactiveDays int
}

// Matches is the step "filter" of use case 6 for one key at now (R34): a
// case-insensitive substring of the name or prefix (Query is lower case),
// the group or the ungrouped keys, the status, an active key at or above
// KeyLimitWarningPercent of its fullest limit, now < expiry ≤ now + N days,
// and no use since now − N days.
func (f KeyListFilter) Matches(key OverviewKey, fullest *LimitState, now time.Time) bool {
	switch {
	case f.Query != "" && !strings.Contains(strings.ToLower(key.Name), f.Query) && !strings.Contains(strings.ToLower(key.Prefix), f.Query):
		return false
	case f.Ungrouped && key.GroupID != nil:
		return false
	case f.GroupID != nil && (key.GroupID == nil || *key.GroupID != *f.GroupID):
		return false
	case f.Status != "" && key.Status(now) != f.Status:
		return false
	case f.NearLimit && (key.Status(now) != KeyStatusActive || fullest == nil || fullest.Percent < KeyLimitWarningPercent):
		return false
	}
	if f.ExpiringDays > 0 {
		limit := now.Add(time.Duration(f.ExpiringDays) * 24 * time.Hour)
		if key.ExpiresAt == nil || !key.ExpiresAt.After(now) || key.ExpiresAt.After(limit) {
			return false
		}
	}
	if f.InactiveDays > 0 {
		since := now.Add(-time.Duration(f.InactiveDays) * 24 * time.Hour)
		if key.LastUsedAt != nil && !key.LastUsedAt.Before(since) {
			return false
		}
	}
	return true
}

// KeyListQuery bounds the keys list's usage read in UTC: DayStarts are the
// local midnights of the days ending today (today last), End the local
// midnight after today; Now projects expired limit windows. EntitiesOnly
// reads the groups and keys without usage (matching ids not sorted by spend).
type KeyListQuery struct {
	DayStarts    []time.Time
	End, Now     time.Time
	EntitiesOnly bool
}

// KeyListData is one analytics snapshot of the keys list: the groups, the
// current keys with projected limits and, per key with usage, the unrounded
// estimated cost of each day of the query. While the rollups are unusable
// only today is read: DaysKnown is false and each cost list holds one entry.
type KeyListData struct {
	Groups    []OverviewGroupRef
	Keys      []OverviewKey
	Costs     map[string][]float64
	DaysKnown bool
}

// KeyLimitSummary is one active limit of a key row.
type KeyLimitSummary struct {
	Type        LimitType   `json:"type"`
	Window      LimitWindow `json:"window"`
	ModelFilter *string     `json:"modelFilter"`
	MaxValue    int64       `json:"maxValue"`
}

// KeyListRow is one row of the keys table; period spend is null while the
// rollups are unusable.
type KeyListRow struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Prefix        string            `json:"prefix"`
	GroupID       *string           `json:"groupId"`
	GroupName     *string           `json:"groupName"`
	Status        string            `json:"status"`
	ExpiresAt     *time.Time        `json:"expiresAt"`
	LastUsedAt    *time.Time        `json:"lastUsedAt"`
	CreatedAt     time.Time         `json:"createdAt"`
	SpendTodayUSD float64           `json:"spendTodayUsd"`
	Spend7dUSD    *float64          `json:"spend7dUsd"`
	Spend30dUSD   *float64          `json:"spend30dUsd"`
	MaxLimit      *LimitState       `json:"maxLimit"`
	LimitsSummary []KeyLimitSummary `json:"limitsSummary"`
}

// KeysPage is GET /api/keys.
type KeysPage struct {
	Shell       ShellStatus  `json:"shell"`
	GeneratedAt time.Time    `json:"generatedAt"`
	Timezone    string       `json:"timezone"`
	Total       int          `json:"total"`
	Offset      int          `json:"offset"`
	Limit       int          `json:"limit"`
	Rows        []KeyListRow `json:"rows"`
}

// KeyIDs is GET /api/keys/ids: at most KeyIDsLimit ids in list order and the
// number of matching keys.
type KeyIDs struct {
	IDs   []string `json:"ids"`
	Total int      `json:"total"`
}

// KeyIDsLimit bounds "Select all matching" and one bulk request.
const KeyIDsLimit = 1000

// KeyBulkAction is an action of POST /api/keys/bulk (use case 6.1).
type KeyBulkAction string

const (
	KeyBulkDisable   KeyBulkAction = "disable"
	KeyBulkEnable    KeyBulkAction = "enable"
	KeyBulkMoveGroup KeyBulkAction = "move_group"
	KeyBulkSetLimits KeyBulkAction = "set_limits"
)

// Per-key failure codes of a bulk action.
const (
	KeyBulkNotFound     = "not_found"
	KeyBulkKeyInGroup   = "key_in_group"
	KeyBulkInvalidGroup = "invalid_group"
	KeyBulkNotApplied   = "not_applied"
	KeyBulkConflict     = "conflict"
	KeyBulkInternal     = "internal_error"
)

// KeyBulkResult is the outcome for one requested id; Code is set on failure.
type KeyBulkResult struct {
	ID   string `json:"id"`
	OK   bool   `json:"ok"`
	Code string `json:"code,omitempty"`
}

type KeyBulkResponse struct {
	Results []KeyBulkResult `json:"results"`
}

var (
	// ErrKeyInGroup refuses own limits for a key whose limits come from its
	// group.
	ErrKeyInGroup = errors.New("API key belongs to a group")
	// ErrUnknownGroup refuses a move to a group that does not exist.
	ErrUnknownGroup = fmt.Errorf("unknown account group: %w", ErrInvalid)
)

// ValidateLimitRules checks each rule and refuses two rules of the same type,
// window and model filter.
func ValidateLimitRules(rules []LimitRule) error {
	seen := make(map[string]bool, len(rules))
	for _, rule := range rules {
		if err := rule.Validate(); err != nil {
			return err
		}
		model := ""
		if rule.ModelFilter != nil {
			model = *rule.ModelFilter
		}
		id := string(rule.Type) + "\x00" + string(rule.Window) + "\x00" + model
		if seen[id] {
			return fmt.Errorf("duplicate limit: %w", ErrInvalidLimit)
		}
		seen[id] = true
	}
	return nil
}
