package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

// keySpanFromEvents is the raw evaluation of keySpan: folded legacy hours and
// every reported event of [from, to) summed per key.
func keySpanFromEvents(ctx context.Context, tx *sql.Tx, from, to int64) (*spanSums, error) {
	sums := &spanSums{keys: map[string]*overviewDay{}}
	if err := sums.addLegacy(ctx, tx, fromMillis(from), fromMillis(to)); err != nil {
		return nil, err
	}
	where, args := reportEventWhere(reportFilterDims{})
	rows, err := tx.QueryContext(ctx, `SELECT e.api_key_id,count(*),coalesce(sum(e.cost_microdollars),0)
 FROM usage_events e INDEXED BY idx_usage_events_time_id WHERE e.requested_at>=? AND e.requested_at<? AND `+where+`
 GROUP BY e.api_key_id`, append([]any{from, to}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key sql.NullString
		var requests, micro int64
		if err := rows.Scan(&key, &requests, &micro); err != nil {
			return nil, err
		}
		sums.add(rawReportDimension(key), requests, micro, 0)
	}
	return sums, rows.Err()
}

// groupsUsageCases are groups summary usage reads whose 30 local days cover
// the autumn and spring fixtures and both daylight-saving changes of Prague,
// for whole-hour, negative, large and fractional offsets.
func groupsUsageCases(t *testing.T, random *rand.Rand) []rollupReadCase {
	t.Helper()
	var cases []rollupReadCase
	for _, zone := range []string{"UTC", "Europe/Prague", "America/New_York", "Asia/Tokyo", "Asia/Kolkata"} {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Fatal(err)
		}
		instants := []time.Time{time.Date(2026, 4, 8, 9, 0, 0, 0, time.UTC), time.Date(2026, 10, 27, 23, 0, 0, 0, time.UTC)}
		for range 2 {
			base := []time.Time{time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)}[random.IntN(2)]
			instants = append(instants, base.Add(time.Duration(random.Int64N(int64(9*24*time.Hour)))))
		}
		for _, now := range instants {
			q := application.GroupsSummaryUsageQuery(now, loc)
			from, to := millis(q.SpanStart), millis(q.Overview.DayStarts[0])
			cases = append(cases, rollupReadCase{
				name: fmt.Sprintf("groups %s %s", zone, now.Format(time.RFC3339Nano)),
				rollup: func(ctx context.Context, r *rollupRead) (any, error) {
					usage, err := r.overviewUsage(ctx, q.Overview)
					if err != nil {
						return nil, err
					}
					span, err := r.keySpan(ctx, from, to)
					if err != nil {
						return nil, err
					}
					return span.result(usage), nil
				},
				oracle: func(ctx context.Context, tx *sql.Tx) (any, error) {
					usage, err := overviewUsageFromEvents(ctx, tx, q.Overview, q.Overview.DayStarts[0])
					if err != nil {
						return nil, err
					}
					usage.RollupsUsable = true
					span, err := keySpanFromEvents(ctx, tx, from, to)
					if err != nil {
						return nil, err
					}
					return span.result(usage), nil
				},
			})
		}
	}
	return cases
}

// The groups summary's usage, the overview's days plus the per-key span
// before them, equals the raw evaluation in every fold state: (a) folded, (b)
// the folder at a random cursor, (c) journal rows pending after retention,
// repricing, moves, deletions and late inserts, and (d) the journal applied
// (R40).
func TestGroupsSummaryUsageMatchesRawEventsInEveryFoldState(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	random := rand.New(rand.NewPCG(71, 72))
	seedRollupEquivalence(t, s, random)
	cases := groupsUsageCases(t, random)
	foldRollups(t, s, 1000)
	assertRollupReadsMatchRaw(t, s, "(a) folded", cases)
	var events int
	if err := s.db.QueryRow(`SELECT count(*) FROM usage_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := s.RebuildUsageRollups(ctx); err != nil {
		t.Fatal(err)
	}
	foldRollupsTo(t, s, 1+random.IntN(events-1))
	assertRollupReadsMatchRaw(t, s, "(b) cursor", cases)
	foldRollups(t, s, 1000)
	for round := range 2 {
		mutateRollupEquivalence(t, s, random, round)
		assertRollupReadsMatchRaw(t, s, fmt.Sprintf("(c) journal after round %d", round), cases)
		foldRollups(t, s, 1000)
		assertRollupReadsMatchRaw(t, s, fmt.Sprintf("(d) applied round %d", round), cases)
	}
	// Sanity: the span sees several keys with usage and legacy hours.
	tx, err := s.analyticsDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	q := application.GroupsSummaryUsageQuery(time.Date(2026, 10, 27, 23, 0, 0, 0, time.UTC), time.UTC)
	span, err := keySpanFromEvents(ctx, tx, millis(q.SpanStart), millis(q.Overview.DayStarts[0]))
	if err != nil {
		t.Fatal(err)
	}
	result := span.result(domain.OverviewUsage{})
	if len(result.SpanKeys) < 2 || result.SpanPool.Requests == 0 || result.SpanPool.CostUSD == 0 {
		t.Fatalf("fixture does not exercise the span: %+v", result)
	}
}

// The span reads UTC-day slice rows for its whole UTC days and, at each
// edge, its hour rows or, past half a day, the edge's UTC-day row minus the
// hour rows outside the span: at most 21 day rows and 24 hour rows per key
// for every zone.
func TestGroupsSummarySpanSlicePlanUsesDayRows(t *testing.T) {
	for _, zone := range []string{"UTC", "Europe/Prague", "America/New_York", "Asia/Tokyo", "Asia/Kolkata", "America/St_Johns"} {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Fatal(err)
		}
		q := application.GroupsSummaryUsageQuery(dashboardNow, loc)
		from, to := millis(q.SpanStart), millis(q.Overview.DayStarts[0])
		b := planSpanSlices(planRollupWindow(from, to, nil))
		if len(b.days) < 20 || len(b.days) > 24 || len(b.hours) > 24 {
			t.Errorf("%s: %d day rows and %d hour rows", zone, len(b.days), len(b.hours))
		}
		// Every hour of the span counts exactly once.
		weight := map[int64]int64{}
		for day, sign := range b.days {
			for h := day; h < day+86400; h += 3600 {
				weight[h] += sign
			}
		}
		for hour, sign := range b.hours {
			weight[hour] += sign
		}
		first, last := floorRollupHour(from+rollupHourMS-1)/1000, floorRollupHour(to)/1000
		for hour, w := range weight {
			if inside := hour >= first && hour < last; inside && w != 1 || !inside && w != 0 {
				t.Fatalf("%s: hour %d counted %d times", zone, hour, w)
			}
		}
		for h := first; h < last; h += 3600 {
			if weight[h] != 1 {
				t.Fatalf("%s: hour %d of the span is missing", zone, h)
			}
		}
	}
}

// While the rollups are unusable the summary follows the overview's raw
// fallback, the span is unknown and every 7-day and 30-day spend is null.
func TestGroupsSummaryUsageFallbackLeavesTheSpanUnknown(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	now := time.Date(2026, 10, 28, 15, 20, 0, 0, time.UTC)
	if _, err := s.db.Exec(`INSERT INTO usage_events(request_id,api_key_id,model,status,request_kind,requested_at,cost_microdollars)
 VALUES('old','key-a','m','success','normal',?,9000000),('today','key-a','m','success','normal',?,2000000)`,
		millis(now.Add(-20*24*time.Hour)), millis(now.Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE usage_rollup_state SET status='rebuilding' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	q := application.GroupsSummaryUsageQuery(now, time.UTC)
	usage, err := s.GroupsSummaryUsage(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	want, err := s.OverviewUsage(ctx, q.Overview)
	if err != nil {
		t.Fatal(err)
	}
	if usage.SpanKnown || usage.SpanKeys != nil || !reflect.DeepEqual(usage.Overview, want) || usage.Overview.DaysKnown {
		t.Fatalf("fallback usage %+v", usage)
	}
	summary := groupsSummaryAt(t, s, now, time.UTC)
	if summary.Pool.Spend30dUSD != nil || summary.Pool.Spend7dUSD != nil || summary.Pool.SpendTodayUSD != 2 {
		t.Fatalf("fallback pool %+v", summary.Pool)
	}
	for _, row := range summary.Groups {
		if row.Spend30dUSD != nil || row.Spend7dUSD != nil {
			t.Fatalf("spend must be null while rollups are unusable: %+v", row)
		}
	}
	for _, bad := range []domain.GroupsSummaryUsageQuery{{Overview: q.Overview}, {Overview: q.Overview, SpanStart: q.Overview.DayStarts[0]},
		{Overview: q.Overview, SpanStart: q.Overview.DayStarts[0].AddDate(0, 0, -41)}} {
		if _, err := s.GroupsSummaryUsage(ctx, bad); err == nil {
			t.Fatalf("span %v accepted", bad.SpanStart)
		}
	}
}

// groupsSummaryAt runs the groups summary use case on a store at now.
func groupsSummaryAt(t testing.TB, s *Store, now time.Time, loc *time.Location) domain.GroupsSummary {
	t.Helper()
	service := application.NewGroupsSummaryService(s, application.NewShellStatusService(s, nil), func() time.Time { return now })
	summary, err := service.Summary(context.Background(), loc)
	if err != nil {
		t.Fatal(err)
	}
	return summary
}

// The entity snapshot is the overview's with every group's templates; a
// group's 30-day spend equals the raw sum of its member keys' events of the
// 30 local days, and its rows equal the overview's (R40).
func TestGroupsSummaryMatchesTheOverviewAndRawThirtyDaySpend(t *testing.T) {
	ctx := context.Background()
	scale := dashboardScale{keys: 30, groups: 5, accounts: 10, days: 31, eventsPerKeyHour: 1}
	if raceDetector {
		scale.days = 9
	}
	s, _ := testStore(t)
	seedDashboardScale(t, s, scale)
	entities, err := s.GroupsSummaryEntities(ctx, dashboardNow)
	if err != nil {
		t.Fatal(err)
	}
	overviewEntities, err := s.OverviewEntities(ctx, dashboardNow)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(entities.OverviewEntities, overviewEntities) || len(entities.Limits) != scale.groups ||
		entities.Limits["grp-1"][0] != (domain.LimitTemplate{Type: domain.LimitCostUSD, Window: domain.WindowWeekly, MaxValue: 50000000}) {
		t.Fatalf("entities %+v", entities.Limits)
	}
	prague, err := time.LoadLocation("Europe/Prague")
	if err != nil {
		t.Fatal(err)
	}
	summary := groupsSummaryAt(t, s, dashboardNow, prague)
	overview := overviewAt(t, s, prague)
	for i, row := range summary.Groups {
		if !reflect.DeepEqual(row.GroupRow, overview.Groups[i]) {
			t.Fatalf("row %d differs from the overview:\n%+v\n%+v", i, row.GroupRow, overview.Groups[i])
		}
	}
	start := application.GroupsSummaryUsageQuery(dashboardNow, prague).SpanStart
	keyGroup := map[string]string{}
	for _, key := range entities.Keys {
		if key.GroupID != nil {
			keyGroup[key.ID] = *key.GroupID
		}
	}
	rows, err := s.db.Query(`SELECT api_key_id,sum(cost_microdollars) FROM usage_events WHERE requested_at>=? AND requested_at<?
 AND request_kind NOT IN ('warmup','limit_warmup') GROUP BY api_key_id`, millis(start), millis(dashboardNow))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]float64{}
	var pool float64
	for rows.Next() {
		var key string
		var micro int64
		if err := rows.Scan(&key, &micro); err != nil {
			t.Fatal(err)
		}
		want[keyGroup[key]] += float64(micro) / 1e6
		pool += float64(micro) / 1e6
	}
	if err := closeRows(rows); err != nil {
		t.Fatal(err)
	}
	for _, row := range summary.Groups {
		id := ""
		if row.ID != nil {
			id = *row.ID
		}
		if row.Spend30dUSD == nil || math.Abs(*row.Spend30dUSD-want[id]) > 1e-5 || want[id] == 0 {
			t.Fatalf("group %q 30-day spend %v, raw %v", id, row.Spend30dUSD, want[id])
		}
		if len(row.TopKeys7d) == 0 || !slices.IsSortedFunc(row.TopKeys7d, func(a, b domain.GroupKey) int {
			if domain.GroupKeyLess(b, a) {
				return 1
			}
			return -1
		}) {
			t.Fatalf("group %q keys %+v", id, row.TopKeys7d)
		}
	}
	if math.Abs(*summary.Pool.Spend30dUSD-pool) > 1e-5 || len(summary.Accounts) != scale.accounts {
		t.Fatalf("pool 30-day spend %v, raw %v; %d accounts", *summary.Pool.Spend30dUSD, pool, len(summary.Accounts))
	}
}

// The groups summary executes the same number of statements for 5 groups,
// 30 keys and 10 accounts as for 50 groups, 300 keys and 100 accounts, in a
// whole-hour and a fractional-offset zone (R40).
func TestGroupsSummaryStatementCountIsIndependentOfScale(t *testing.T) {
	prague, _ := time.LoadLocation("Europe/Prague")
	kolkata, _ := time.LoadLocation("Asia/Kolkata")
	counts := map[string][]int64{}
	for _, scale := range []dashboardScale{{keys: 30, groups: 5, accounts: 10, days: 2, eventsPerKeyHour: 1},
		{keys: 300, groups: 50, accounts: 100, days: 2, eventsPerKeyHour: 1}} {
		s, counter := countingStore(t)
		seedDashboardScale(t, s, scale)
		for name, loc := range map[string]*time.Location{"prague": prague, "kolkata": kolkata} {
			before := counter.count()
			summary := groupsSummaryAt(t, s, dashboardNow, loc)
			if len(summary.Groups) != scale.groups+1 {
				t.Fatalf("%d rows for %d groups", len(summary.Groups), scale.groups)
			}
			counts[name] = append(counts[name], counter.count()-before)
		}
	}
	for name, n := range counts {
		t.Logf("groups summary %s: %v statements", name, n)
		if n[0] != n[1] || n[0] == 0 {
			t.Errorf("%s: %d statements at small scale, %d at target scale", name, n[0], n[1])
		}
	}
}

// The groups summary answers within the 300 ms page budget, warm, for 50
// groups, 300 keys and 100 accounts with every key active in every hour of
// 12 days, which puts usage into the span before the overview's days (R49);
// BenchmarkDashboardPages measures all 30 days. The race detector slows
// SQLite about 40-fold, so it only checks the shape there.
func TestGroupsSummaryMeetsItsLatencyBudgetAtTargetScale(t *testing.T) {
	scale := dashboardScale{keys: 300, groups: 50, accounts: 100, days: 12, eventsPerKeyHour: 1}
	if raceDetector {
		scale.days = 1
	}
	s, _ := testStore(t)
	seedDashboardScale(t, s, scale)
	prague, _ := time.LoadLocation("Europe/Prague")
	summary := groupsSummaryAt(t, s, dashboardNow, prague)
	if !summary.Shell.RollupsUsable || len(summary.Groups) != scale.groups+1 || summary.Pool.Spend30dUSD == nil {
		t.Fatalf("unexpected summary: usable %v, %d rows", summary.Shell.RollupsUsable, len(summary.Groups))
	}
	var runs []time.Duration
	for range 5 {
		start := time.Now()
		groupsSummaryAt(t, s, dashboardNow, prague)
		runs = append(runs, time.Since(start))
	}
	slices.Sort(runs)
	t.Logf("groups summary at 50 groups / 300 keys / 100 accounts, %d events: median %v (min %v, max %v)",
		scale.keys*scale.days*24*scale.eventsPerKeyHour, runs[2], runs[0], runs[4])
	if !raceDetector && runs[2] > 300*time.Millisecond {
		t.Fatalf("groups summary median %v exceeds the 300 ms budget", runs[2])
	}
}

// benchmarkGroupsSummary adds the Groups page endpoint to
// BenchmarkDashboardPages in a whole-hour, a daylight-saving and a
// fractional-offset zone.
func benchmarkGroupsSummary(b *testing.B, s *Store) {
	for _, zone := range []string{"UTC", "Europe/Prague", "Asia/Kolkata"} {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			b.Fatal(err)
		}
		b.Run("groups_summary_"+zone, func(b *testing.B) {
			for range b.N {
				groupsSummaryAt(b, s, dashboardNow, loc)
			}
		})
	}
}
