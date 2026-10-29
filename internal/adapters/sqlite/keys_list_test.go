package sqlite

import (
	"context"
	"database/sql"
	"reflect"
	"sort"
	"testing"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

func keysListService(s *Store) *application.KeysListService {
	return application.NewKeysListService(s, application.NewShellStatusService(s, nil), func() time.Time { return dashboardNow })
}

// The keys list page and its matching ids execute the same number of
// statements for 30 keys as for 300 keys (R33).
func TestKeysListUsesAStatementCountIndependentOfScale(t *testing.T) {
	prague, err := time.LoadLocation("Europe/Prague")
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string][]int64{}
	for _, scale := range []dashboardScale{{keys: 30, groups: 5, accounts: 10, days: 1, eventsPerKeyHour: 1},
		{keys: 300, groups: 50, accounts: 100, days: 1, eventsPerKeyHour: 1}} {
		s, counter := countingStore(t)
		seedDashboardScale(t, s, scale)
		service := keysListService(s)
		measure := func(name string, read func() int) {
			before := counter.count()
			if total := read(); total != scale.keys {
				t.Fatalf("%s: %d keys listed of %d", name, total, scale.keys)
			}
			counts[name] = append(counts[name], counter.count()-before)
		}
		for name, params := range map[string]application.KeysListParams{
			"page by spend": {Sort: domain.KeySortSpend30d, Location: prague},
			"page by name":  {Location: prague, Offset: 50},
		} {
			measure(name, func() int {
				page, err := service.List(context.Background(), params)
				if err != nil {
					t.Fatal(err)
				}
				return page.Total
			})
		}
		for name, sort := range map[string]domain.KeyListSort{"ids by name": domain.KeySortName, "ids by spend": domain.KeySortSpendToday} {
			measure(name, func() int {
				ids, err := service.IDs(context.Background(), application.KeysListParams{Sort: sort, Location: prague})
				if err != nil {
					t.Fatal(err)
				}
				return ids.Total
			})
		}
	}
	for name, n := range counts {
		t.Logf("%s: %d statements at both scales", name, n[1])
		if n[0] != n[1] || n[0] == 0 {
			t.Errorf("%s: %d statements at small scale, %d at target scale", name, n[0], n[1])
		}
	}
}

// The keys list reads per-key daily cost equal to the raw evaluation of the
// same days, lists no deleted or internal key, and while the rollups are
// unusable reads today only, equal to the rollup path's today.
func TestKeyListMatchesRawEventsAndFallsBackToToday(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	seedDashboardScale(t, s, dashboardScale{keys: 12, groups: 3, accounts: 4, days: 3, eventsPerKeyHour: 1})
	prague, err := time.LoadLocation("Europe/Prague")
	if err != nil {
		t.Fatal(err)
	}
	var q domain.KeyListQuery
	local := dashboardNow.In(prague)
	for i := range 30 {
		q.DayStarts = append(q.DayStarts, time.Date(local.Year(), local.Month(), local.Day()-29+i, 0, 0, 0, 0, prague).UTC())
	}
	q.End = time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, prague).UTC()
	q.Now = dashboardNow
	data, err := s.KeyList(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if !data.DaysKnown || len(data.Keys) != 12 || len(data.Groups) != 3 {
		t.Fatalf("rollup read: known %v, %d keys, %d groups", data.DaysKnown, len(data.Keys), len(data.Groups))
	}
	for _, key := range data.Keys {
		if key.ID == "key-deleted" || domain.IsInternalKey(key.ID) {
			t.Fatalf("listed %s", key.ID)
		}
	}
	tx, err := s.analyticsDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	oracle, err := newKeyUsageSums(ctx, tx, domain.KeyUsageQuery{DayStarts: q.DayStarts, End: q.End, Now: q.Now, SkipPrevious: true})
	if err == nil {
		err = oracle.readEvents(ctx, tx)
	}
	_ = tx.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	want := keyListData(oracle.result(false), true)
	if !reflect.DeepEqual(data.Costs, want.Costs) {
		t.Fatalf("rollup costs differ from raw events:\n got %v\nwant %v", data.Costs, want.Costs)
	}
	active := 0
	for _, costs := range data.Costs {
		if costs[29] > 0 && costs[28] > 0 {
			active++
		}
	}
	if active != 12 {
		t.Fatalf("fixture spends on today and yesterday for %d keys", active)
	}
	if _, err := s.db.Exec(`UPDATE usage_rollup_state SET status='rebuilding' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	fallback, err := s.KeyList(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if fallback.DaysKnown || len(fallback.Keys) != 12 {
		t.Fatalf("fallback: known %v, %d keys", fallback.DaysKnown, len(fallback.Keys))
	}
	for id, costs := range data.Costs {
		if got := fallback.Costs[id]; len(got) != 1 || got[0] != costs[29] {
			t.Fatalf("fallback today of %s: %v, rollups %v", id, got, costs[29])
		}
	}
}

// The keys list answers within the 300 ms budget, warm, for 300 keys and 50
// groups with every key active in every hour of the 8 days before now (R49);
// under the race detector only the shape is checked.
func TestKeysListMeetsItsLatencyBudgetAtTargetScale(t *testing.T) {
	scale := dashboardScale{keys: 300, groups: 50, accounts: 100, days: 8, eventsPerKeyHour: 1}
	if raceDetector {
		scale.days = 1
	}
	s, _ := testStore(t)
	seedDashboardScale(t, s, scale)
	service := keysListService(s)
	params := application.KeysListParams{Sort: domain.KeySortSpend30d}
	page, err := service.List(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 300 || len(page.Rows) != 50 || page.Rows[0].Spend30dUSD == nil || !page.Shell.RollupsUsable {
		t.Fatalf("unexpected keys page: total %d, %d rows, usable %v", page.Total, len(page.Rows), page.Shell.RollupsUsable)
	}
	var runs []time.Duration
	for range 5 {
		start := time.Now()
		if _, err := service.List(context.Background(), params); err != nil {
			t.Fatal(err)
		}
		runs = append(runs, time.Since(start))
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i] < runs[j] })
	t.Logf("keys list at 300 keys / 50 groups, %d events: median %v (min %v, max %v)",
		scale.keys*scale.days*24*scale.eventsPerKeyHour, runs[2], runs[0], runs[4])
	if !raceDetector && runs[2] > 300*time.Millisecond {
		t.Fatalf("keys list median %v exceeds the 300 ms budget", runs[2])
	}
}

// benchmarkKeysList is the keys list part of BenchmarkDashboardPages.
func benchmarkKeysList(b *testing.B, s *Store) {
	service := keysListService(s)
	for _, zone := range []string{"UTC", "Europe/Prague", "Asia/Kolkata"} {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			b.Fatal(err)
		}
		for name, params := range map[string]application.KeysListParams{
			"keys_list_spend_30d_":  {Sort: domain.KeySortSpend30d, Location: loc},
			"keys_list_near_limit_": {Filter: domain.KeyListFilter{NearLimit: true}, Sort: domain.KeySortLimitFill, Location: loc},
		} {
			b.Run(name+zone, func(b *testing.B) {
				for range b.N {
					if _, err := service.List(context.Background(), params); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
	b.Run("keys_ids_by_name", func(b *testing.B) {
		for range b.N {
			if _, err := service.IDs(context.Background(), application.KeysListParams{}); err != nil {
				b.Fatal(err)
			}
		}
	})
}
