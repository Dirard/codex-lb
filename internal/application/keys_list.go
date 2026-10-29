package application

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"codex-lb/internal/domain"
)

// KeysListRepository is the storage the keys list reads: one analytics
// snapshot of the groups, the current keys with their limits and the per-key
// cost of the local days ending today.
type KeysListRepository interface {
	KeyList(context.Context, domain.KeyListQuery) (domain.KeyListData, error)
}

// ErrKeyFilters is an invalid keys list request; it wraps domain.ErrInvalid.
var ErrKeyFilters = fmt.Errorf("key filters: %w", domain.ErrInvalid)

// Bounds of the keys list (use case 6, Q10: at most 5 000 keys are filtered
// and sorted in memory).
const (
	KeysPageDefaultLimit = 50
	KeysPageMaxLimit     = 100
	keysMaxFilterDays    = 365
	keysMaxTextLength    = 256
	keysSpendDays        = 30
	keysWeekDays         = 7
)

// KeysListParams are the transport parameters of the keys list; a zero Sort
// is by name, a zero Order is ascending by name and descending otherwise, a
// zero Limit is KeysPageDefaultLimit and a nil Location is UTC.
type KeysListParams struct {
	Filter        domain.KeyListFilter
	Sort          domain.KeyListSort
	Order         string
	Offset, Limit int
	Location      *time.Location
}

// KeysListService is use case 6 of the dashboard redesign: one page of the
// keys matching a search and filters, sorted on the server, and the ids of
// every matching key for "Select all matching".
type KeysListService struct {
	repo  KeysListRepository
	shell *ShellStatusService
	now   func() time.Time
}

func NewKeysListService(repo KeysListRepository, shell *ShellStatusService, now func() time.Time) *KeysListService {
	if now == nil {
		now = time.Now
	}
	return &KeysListService{repo: repo, shell: shell, now: now}
}

// keysPlan is a validated keys list request.
type keysPlan struct {
	filter        domain.KeyListFilter
	sort          domain.KeyListSort
	desc          bool
	offset, limit int
	loc           *time.Location
	now           time.Time
}

// plan validates the request: known sort, order and status, day filters of 1
// to 365 days, bounded texts, offset from 0 and a limit of 1 to 100.
func (s *KeysListService) plan(p KeysListParams) (keysPlan, error) {
	plan := keysPlan{filter: p.Filter, sort: p.Sort, offset: p.Offset, limit: p.Limit, loc: p.Location, now: s.now().UTC()}
	if plan.loc == nil {
		plan.loc = time.UTC
	}
	if plan.sort == "" {
		plan.sort = domain.KeySortName
	}
	if plan.limit == 0 {
		plan.limit = KeysPageDefaultLimit
	}
	f := &plan.filter
	f.Query = strings.ToLower(strings.TrimSpace(f.Query))
	switch {
	case !plan.sort.Valid():
		return plan, fmt.Errorf("sort %q: %w", plan.sort, ErrKeyFilters)
	case p.Order != "" && p.Order != "asc" && p.Order != "desc":
		return plan, fmt.Errorf("order %q: %w", p.Order, ErrKeyFilters)
	case plan.offset < 0 || plan.limit < 1 || plan.limit > KeysPageMaxLimit:
		return plan, fmt.Errorf("offset or limit: %w", ErrKeyFilters)
	case utf8.RuneCountInString(f.Query) > keysMaxTextLength:
		return plan, fmt.Errorf("query too long: %w", ErrKeyFilters)
	case f.GroupID != nil && (*f.GroupID == "" || len(*f.GroupID) > keysMaxTextLength || f.Ungrouped):
		return plan, fmt.Errorf("group: %w", ErrKeyFilters)
	case f.Status != "" && f.Status != domain.KeyStatusActive && f.Status != domain.KeyStatusDisabled && f.Status != domain.KeyStatusExpired:
		return plan, fmt.Errorf("status %q: %w", f.Status, ErrKeyFilters)
	case f.ExpiringDays < 0 || f.ExpiringDays > keysMaxFilterDays || f.InactiveDays < 0 || f.InactiveDays > keysMaxFilterDays:
		return plan, fmt.Errorf("days: %w", ErrKeyFilters)
	}
	plan.desc = p.Order == "desc" || (p.Order == "" && plan.sort != domain.KeySortName)
	return plan, nil
}

// query is the step "local day bounds" of use case 6: the local midnights of
// the 30 days ending today and the midnight after today.
func (p keysPlan) query(entitiesOnly bool) domain.KeyListQuery {
	local := p.now.In(p.loc)
	q := domain.KeyListQuery{Now: p.now, EntitiesOnly: entitiesOnly}
	if entitiesOnly {
		return q
	}
	for i := range keysSpendDays {
		day := time.Date(local.Year(), local.Month(), local.Day()-(keysSpendDays-1)+i, 0, 0, 0, 0, p.loc)
		q.DayStarts = append(q.DayStarts, day.UTC())
	}
	q.End = time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, p.loc).UTC()
	return q
}

// keyEntry is one key with what its filters and sorts read.
type keyEntry struct {
	key         domain.OverviewKey
	lowerName   string
	fullest     *domain.LimitState
	today       float64
	week, month *float64
}

// matching is the workflow "filter, then sort with the tie-break by id" over
// one snapshot.
func (p keysPlan) matching(data domain.KeyListData) []keyEntry {
	entries := make([]keyEntry, 0, len(data.Keys))
	for _, key := range data.Keys {
		fullest := domain.FullestLimit(key.Limits)
		if !p.filter.Matches(key, fullest, p.now) {
			continue
		}
		entry := keyEntry{key: key, lowerName: strings.ToLower(key.Name), fullest: fullest}
		costs := data.Costs[key.ID]
		if len(costs) > 0 {
			entry.today = roundTo(costs[len(costs)-1], 6)
		}
		if data.DaysKnown {
			entry.week = sumCosts(costs, keysWeekDays)
			entry.month = sumCosts(costs, keysSpendDays)
		}
		entries = append(entries, entry)
	}
	slices.SortFunc(entries, func(a, b keyEntry) int { return compareKeyEntries(&a, &b, p.sort, p.desc) })
	return entries
}

// sumCosts is the rounded sum of the last n daily costs.
func sumCosts(costs []float64, n int) *float64 {
	var sum float64
	for _, cost := range costs[max(0, len(costs)-n):] {
		sum += cost
	}
	total := roundTo(sum, 6)
	return &total
}

// compareKeyEntries orders by the sort key in the requested direction, keys
// without a value for it last in either direction, then by id ascending.
func compareKeyEntries(a, b *keyEntry, sort domain.KeyListSort, desc bool) int {
	direct := func(c int) int {
		if desc {
			return -c
		}
		return c
	}
	optional := func(av, bv *float64) int {
		switch {
		case av == nil && bv == nil:
			return 0
		case av == nil:
			return 1
		case bv == nil:
			return -1
		}
		return direct(cmp.Compare(*av, *bv))
	}
	var c int
	switch sort {
	case domain.KeySortName:
		c = direct(cmp.Or(cmp.Compare(a.lowerName, b.lowerName), cmp.Compare(a.key.Name, b.key.Name)))
	case domain.KeySortCreated:
		c = direct(a.key.CreatedAt.Compare(b.key.CreatedAt))
	case domain.KeySortSpendToday:
		c = direct(cmp.Compare(a.today, b.today))
	case domain.KeySortSpend7d:
		c = optional(a.week, b.week)
	case domain.KeySortSpend30d:
		c = optional(a.month, b.month)
	case domain.KeySortLimitFill:
		c = optional(limitPercent(a.fullest), limitPercent(b.fullest))
	case domain.KeySortLastUsed:
		c = optional(unixMilli(a.key.LastUsedAt), unixMilli(b.key.LastUsedAt))
	}
	return cmp.Or(c, cmp.Compare(a.key.ID, b.key.ID))
}

func limitPercent(limit *domain.LimitState) *float64 {
	if limit == nil {
		return nil
	}
	return &limit.Percent
}

func unixMilli(at *time.Time) *float64 {
	if at == nil {
		return nil
	}
	value := float64(at.UnixMilli())
	return &value
}

// List returns one page of the matching keys with the total before paging
// and the shell status.
func (s *KeysListService) List(ctx context.Context, p KeysListParams) (domain.KeysPage, error) {
	plan, err := s.plan(p)
	if err != nil {
		return domain.KeysPage{}, err
	}
	data, err := s.repo.KeyList(ctx, plan.query(false))
	if err != nil {
		return domain.KeysPage{}, err
	}
	shell, err := s.shell.Status(ctx)
	if err != nil {
		return domain.KeysPage{}, err
	}
	entries := plan.matching(data)
	page := domain.KeysPage{Shell: shell, GeneratedAt: plan.now, Timezone: plan.loc.String(), Total: len(entries),
		Offset: plan.offset, Limit: plan.limit, Rows: []domain.KeyListRow{}}
	groupNames := make(map[string]string, len(data.Groups))
	for _, group := range data.Groups {
		groupNames[group.ID] = group.Name
	}
	if plan.offset >= len(entries) {
		return page, nil
	}
	for _, entry := range entries[plan.offset:min(len(entries), plan.offset+plan.limit)] {
		page.Rows = append(page.Rows, keyRow(entry, groupNames, plan.now))
	}
	return page, nil
}

// IDs returns the ids of the matching keys in list order, at most
// domain.KeyIDsLimit, and their number. Usage is read only for a spend sort.
func (s *KeysListService) IDs(ctx context.Context, p KeysListParams) (domain.KeyIDs, error) {
	p.Offset, p.Limit = 0, 0
	plan, err := s.plan(p)
	if err != nil {
		return domain.KeyIDs{}, err
	}
	data, err := s.repo.KeyList(ctx, plan.query(!plan.sort.UsesSpend()))
	if err != nil {
		return domain.KeyIDs{}, err
	}
	entries := plan.matching(data)
	result := domain.KeyIDs{IDs: make([]string, 0, min(len(entries), domain.KeyIDsLimit)), Total: len(entries)}
	for _, entry := range entries[:min(len(entries), domain.KeyIDsLimit)] {
		result.IDs = append(result.IDs, entry.key.ID)
	}
	return result, nil
}

func keyRow(entry keyEntry, groupNames map[string]string, now time.Time) domain.KeyListRow {
	key := entry.key
	row := domain.KeyListRow{ID: key.ID, Name: key.Name, Prefix: key.Prefix, GroupID: key.GroupID, Status: key.Status(now),
		ExpiresAt: key.ExpiresAt, LastUsedAt: key.LastUsedAt, CreatedAt: key.CreatedAt, SpendTodayUSD: entry.today,
		Spend7dUSD: entry.week, Spend30dUSD: entry.month, MaxLimit: entry.fullest,
		LimitsSummary: make([]domain.KeyLimitSummary, 0, len(key.Limits))}
	if key.GroupID != nil {
		if name, ok := groupNames[*key.GroupID]; ok {
			row.GroupName = &name
		}
	}
	if row.MaxLimit != nil {
		limit := *row.MaxLimit
		limit.Percent = roundTo(limit.Percent, 2)
		row.MaxLimit = &limit
	}
	for _, limit := range key.Limits {
		row.LimitsSummary = append(row.LimitsSummary, domain.KeyLimitSummary{Type: limit.Type, Window: limit.Window,
			ModelFilter: limit.ModelFilter, MaxValue: limit.MaxValue})
	}
	return row
}
