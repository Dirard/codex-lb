package application

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

var keysTestNow = time.Date(2026, 10, 28, 10, 37, 0, 0, time.UTC)

type keysListFake struct {
	overviewFake
	data    domain.KeyListData
	queries []domain.KeyListQuery
}

func (f *keysListFake) KeyList(_ context.Context, q domain.KeyListQuery) (domain.KeyListData, error) {
	f.queries = append(f.queries, q)
	return f.data, nil
}

func newKeysListFake(keys []domain.OverviewKey, costs map[string][]float64) (*keysListFake, *KeysListService) {
	fake := &keysListFake{data: domain.KeyListData{Groups: []domain.OverviewGroupRef{{ID: "grp-a", Name: "Alpha"}},
		Keys: keys, Costs: costs, DaysKnown: true}}
	return fake, NewKeysListService(fake, NewShellStatusService(fake, nil), func() time.Time { return keysTestNow })
}

func listKey(id string, edit func(*domain.OverviewKey)) domain.OverviewKey {
	key := domain.OverviewKey{ID: id, Name: "Key " + id, Prefix: "sk-clb-" + id, IsActive: true,
		CreatedAt: keysTestNow.Add(-time.Hour), Limits: []domain.LimitRule{}}
	if edit != nil {
		edit(&key)
	}
	return key
}

func limitAt(current int64) []domain.LimitRule {
	return []domain.LimitRule{{Type: domain.LimitCostUSD, Window: domain.WindowWeekly, MaxValue: 10000, CurrentValue: current,
		ResetAt: keysTestNow.Add(48 * time.Hour)}}
}

func at(offset time.Duration) *time.Time {
	value := keysTestNow.Add(offset)
	return &value
}

func listIDs(t *testing.T, service *KeysListService, p KeysListParams) []string {
	t.Helper()
	page, err := service.List(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(page.Rows))
	for _, row := range page.Rows {
		ids = append(ids, row.ID)
	}
	if page.Total < len(ids) {
		t.Fatalf("total %d below %d rows", page.Total, len(ids))
	}
	return ids
}

// Every filter of R34 with its boundaries: inactive since exactly N days is
// not inactive, expiry exactly N days ahead is expiring, an expired key is
// not expiring, near limit needs an active key at 90 % or more, disabled
// wins over expired, and the search matches the name or prefix in any case.
func TestKeysListFiltersAndBoundaries(t *testing.T) {
	group := "grp-a"
	day := 24 * time.Hour
	keys := []domain.OverviewKey{
		listKey("used-exactly-7d", func(k *domain.OverviewKey) { k.LastUsedAt = at(-7 * day) }),
		listKey("used-before-7d", func(k *domain.OverviewKey) { k.LastUsedAt = at(-7*day - time.Millisecond) }),
		listKey("never-used", nil),
		listKey("expires-in-7d", func(k *domain.OverviewKey) { k.ExpiresAt = at(7 * day); k.LastUsedAt = at(-time.Minute) }),
		listKey("expires-after-7d", func(k *domain.OverviewKey) {
			k.ExpiresAt = at(7*day + time.Millisecond)
			k.LastUsedAt = at(-time.Minute)
		}),
		listKey("expired", func(k *domain.OverviewKey) { k.ExpiresAt = at(-time.Second); k.LastUsedAt = at(-time.Minute) }),
		listKey("expires-now", func(k *domain.OverviewKey) { k.ExpiresAt = at(0); k.LastUsedAt = at(-time.Minute) }),
		listKey("near", func(k *domain.OverviewKey) {
			k.Limits = limitAt(9000)
			k.LastUsedAt = at(-time.Minute)
			k.GroupID = &group
		}),
		listKey("below", func(k *domain.OverviewKey) { k.Limits = limitAt(8999); k.LastUsedAt = at(-time.Minute) }),
		listKey("disabled-near", func(k *domain.OverviewKey) {
			k.IsActive = false
			k.Limits = limitAt(9900)
			k.ExpiresAt = at(-day)
			k.LastUsedAt = at(-time.Minute)
		}),
		listKey("MixedCase", func(k *domain.OverviewKey) {
			k.Name = "Billing BOT"
			k.Prefix = "sk-clb-ZZtop"
			k.LastUsedAt = at(-time.Minute)
		}),
	}
	_, service := newKeysListFake(keys, nil)
	byID := func(ids ...string) []string { slices.Sort(ids); return ids }
	sorted := func(p KeysListParams) []string {
		p.Sort, p.Order = domain.KeySortName, "asc"
		ids := listIDs(t, service, p)
		slices.Sort(ids)
		return ids
	}
	for name, test := range map[string]struct {
		filter domain.KeyListFilter
		want   []string
	}{
		"inactive 7 days":    {domain.KeyListFilter{InactiveDays: 7}, byID("used-before-7d", "never-used")},
		"expiring 7 days":    {domain.KeyListFilter{ExpiringDays: 7}, byID("expires-in-7d")},
		"expired":            {domain.KeyListFilter{Status: domain.KeyStatusExpired}, byID("expired", "expires-now")},
		"disabled":           {domain.KeyListFilter{Status: domain.KeyStatusDisabled}, byID("disabled-near")},
		"near limit":         {domain.KeyListFilter{NearLimit: true}, byID("near")},
		"group":              {domain.KeyListFilter{GroupID: &group}, byID("near")},
		"ungrouped and near": {domain.KeyListFilter{Ungrouped: true, NearLimit: true}, []string{}},
		"search name":        {domain.KeyListFilter{Query: "  billing bot "}, byID("MixedCase")},
		"search prefix":      {domain.KeyListFilter{Query: "zztop"}, byID("MixedCase")},
		"search and status": {domain.KeyListFilter{Query: "expire", Status: domain.KeyStatusActive},
			byID("expires-in-7d", "expires-after-7d")},
	} {
		got := sorted(KeysListParams{Filter: test.filter})
		if !slices.Equal(got, test.want) {
			t.Errorf("%s: got %v, want %v", name, got, test.want)
		}
	}
	page, err := service.List(context.Background(), KeysListParams{Filter: domain.KeyListFilter{Status: domain.KeyStatusActive}})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 8 {
		t.Fatalf("active total %d, want 8", page.Total)
	}
}

// Sorts order by their key with ties broken by id in both directions; keys
// without a limit, a use or a known period spend come last in both
// directions; the name sort ignores case (R33).
func TestKeysListSortsWithTiesAndMissingValues(t *testing.T) {
	keys := []domain.OverviewKey{
		listKey("k3", func(k *domain.OverviewKey) { k.Name = "beta"; k.Limits = limitAt(5000); k.LastUsedAt = at(-time.Hour) }),
		listKey("k1", func(k *domain.OverviewKey) { k.Name = "Alpha"; k.Limits = limitAt(5000) }),
		listKey("k2", func(k *domain.OverviewKey) { k.Name = "alpha"; k.LastUsedAt = at(-time.Minute) }),
		listKey("k4", func(k *domain.OverviewKey) { k.Name = "Gamma"; k.Limits = limitAt(9000); k.LastUsedAt = at(-time.Hour) }),
	}
	costs := map[string][]float64{"k1": {1, 2}, "k2": {3}, "k3": {0.5, 2}, "k4": {4, 1}}
	for i := range costs {
		costs[i] = append(make([]float64, keysSpendDays-len(costs[i])), costs[i]...)
	}
	_, service := newKeysListFake(keys, costs)
	for name, test := range map[string]struct {
		sort  domain.KeyListSort
		order string
		want  []string
	}{
		"name":            {domain.KeySortName, "", []string{"k1", "k2", "k3", "k4"}},
		"name desc":       {domain.KeySortName, "desc", []string{"k4", "k3", "k2", "k1"}},
		"spend today":     {"spend_today", "", []string{"k2", "k1", "k3", "k4"}},
		"spend today asc": {"spend_today", "asc", []string{"k4", "k1", "k3", "k2"}},
		"spend 7d":        {"spend_7d", "", []string{"k4", "k1", "k2", "k3"}},
		"limit fill":      {"limit_fill", "", []string{"k4", "k1", "k3", "k2"}},
		"limit fill asc":  {"limit_fill", "asc", []string{"k1", "k3", "k4", "k2"}},
		"last used":       {"last_used", "", []string{"k2", "k3", "k4", "k1"}},
		"last used asc":   {"last_used", "asc", []string{"k3", "k4", "k2", "k1"}},
		"created ties":    {"created", "", []string{"k1", "k2", "k3", "k4"}},
	} {
		if got := listIDs(t, service, KeysListParams{Sort: test.sort, Order: test.order}); !slices.Equal(got, test.want) {
			t.Errorf("%s: got %v, want %v", name, got, test.want)
		}
	}
}

// Pages split the sorted list without overlap, the total is counted before
// paging, an offset past the end returns no rows, and the period spend is
// the rounded sum of the last 7 and 30 local days (R33).
func TestKeysListPagesAndSpend(t *testing.T) {
	var keys []domain.OverviewKey
	costs := map[string][]float64{}
	for i := range 120 {
		id := fmt.Sprintf("key-%03d", i)
		keys = append(keys, listKey(id, nil))
		day := make([]float64, keysSpendDays)
		for d := range day {
			day[d] = 0.001 * float64(i%3)
		}
		costs[id] = day
	}
	fake, service := newKeysListFake(keys, costs)
	var seen []string
	for offset := 0; offset < 150; offset += 50 {
		page, err := service.List(context.Background(), KeysListParams{Sort: domain.KeySortSpendToday, Offset: offset})
		if err != nil {
			t.Fatal(err)
		}
		if page.Total != 120 || page.Offset != offset || page.Limit != KeysPageDefaultLimit {
			t.Fatalf("page %d: total %d offset %d limit %d", offset, page.Total, page.Offset, page.Limit)
		}
		for _, row := range page.Rows {
			seen = append(seen, row.ID)
		}
		if offset == 50 && (len(page.Rows) != 50 || *page.Rows[0].Spend30dUSD != 0.03 || *page.Rows[0].Spend7dUSD != 0.007 ||
			page.Rows[0].SpendTodayUSD != 0.001) {
			t.Fatalf("second page %d rows, first %+v", len(page.Rows), page.Rows[0])
		}
	}
	if len(seen) != 120 || len(slices.Compact(slices.Sorted(slices.Values(seen)))) != 120 {
		t.Fatalf("%d rows over the pages", len(seen))
	}
	if seen[0] != "key-002" || seen[1] != "key-005" {
		t.Fatalf("spend ties are not ordered by id: %v", seen[:3])
	}
	page, err := service.List(context.Background(), KeysListParams{Offset: 500, Limit: 100})
	if err != nil || page.Total != 120 || len(page.Rows) != 0 {
		t.Fatalf("past the end: %v, %+v", err, page)
	}
	q := fake.queries[0]
	if len(q.DayStarts) != keysSpendDays || !q.DayStarts[keysSpendDays-1].Equal(time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)) ||
		!q.End.Equal(time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)) || q.EntitiesOnly {
		t.Fatalf("query %+v", q)
	}
	fake.data.DaysKnown = false
	fake.data.Costs = map[string][]float64{"key-001": {0.25}}
	page, err = service.List(context.Background(), KeysListParams{Sort: domain.KeySortSpendToday, Limit: 1})
	if err != nil || page.Rows[0].ID != "key-001" || page.Rows[0].SpendTodayUSD != 0.25 || page.Rows[0].Spend7dUSD != nil ||
		page.Rows[0].Spend30dUSD != nil {
		t.Fatalf("rollups unusable: %v %+v", err, page.Rows)
	}
}

// The local days follow the zone, across the daylight-saving change.
func TestKeysListLocalDays(t *testing.T) {
	prague, err := time.LoadLocation("Europe/Prague")
	if err != nil {
		t.Fatal(err)
	}
	fake, service := newKeysListFake(nil, nil)
	if _, err := service.List(context.Background(), KeysListParams{Location: prague}); err != nil {
		t.Fatal(err)
	}
	q := fake.queries[0]
	if !q.DayStarts[keysSpendDays-1].Equal(time.Date(2026, 10, 27, 23, 0, 0, 0, time.UTC)) ||
		!q.DayStarts[0].Equal(time.Date(2026, 9, 28, 22, 0, 0, 0, time.UTC)) {
		t.Fatalf("Prague days %v … %v", q.DayStarts[0], q.DayStarts[keysSpendDays-1])
	}
}

// Invalid parameters are refused with ErrKeyFilters before any read.
func TestKeysListRejectsInvalidParameters(t *testing.T) {
	fake, service := newKeysListFake(nil, nil)
	empty, named := "", "grp-a"
	long := string(make([]byte, 257))
	for name, p := range map[string]KeysListParams{
		"sort":           {Sort: "spend"},
		"order":          {Order: "up"},
		"limit":          {Limit: 101},
		"negative limit": {Limit: -1},
		"offset":         {Offset: -1},
		"status":         {Filter: domain.KeyListFilter{Status: "paused"}},
		"expiring":       {Filter: domain.KeyListFilter{ExpiringDays: 366}},
		"inactive":       {Filter: domain.KeyListFilter{InactiveDays: -7}},
		"blank group":    {Filter: domain.KeyListFilter{GroupID: &empty}},
		"both groups":    {Filter: domain.KeyListFilter{GroupID: &named, Ungrouped: true}},
		"long query":     {Filter: domain.KeyListFilter{Query: long + "x"}},
	} {
		if _, err := service.List(context.Background(), p); !errors.Is(err, ErrKeyFilters) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(fake.queries) != 0 {
		t.Fatalf("%d reads for invalid requests", len(fake.queries))
	}
}

// Matching ids follow the list order, stop at 1 000 with the full total and
// skip the usage read unless the sort needs spend.
func TestKeysListIDs(t *testing.T) {
	var keys []domain.OverviewKey
	for i := range 1200 {
		keys = append(keys, listKey(fmt.Sprintf("key-%04d", i), nil))
	}
	fake, service := newKeysListFake(keys, map[string][]float64{"key-1100": {5}})
	ids, err := service.IDs(context.Background(), KeysListParams{Order: "desc", Offset: 7, Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if ids.Total != 1200 || len(ids.IDs) != domain.KeyIDsLimit || ids.IDs[0] != "key-1199" || !fake.queries[0].EntitiesOnly {
		t.Fatalf("ids total %d, %d ids, first %s, query %+v", ids.Total, len(ids.IDs), ids.IDs[0], fake.queries[0])
	}
	ids, err = service.IDs(context.Background(), KeysListParams{Sort: domain.KeySortSpendToday})
	if err != nil || ids.IDs[0] != "key-1100" || fake.queries[1].EntitiesOnly {
		t.Fatalf("spend ids: %v first %v", err, ids.IDs[:1])
	}
}
