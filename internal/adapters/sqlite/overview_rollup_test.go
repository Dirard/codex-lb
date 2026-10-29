package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

// overviewUsageCases are overview usage reads at random and hour-aligned
// instants whose 8 local days span both daylight-saving changes of Prague,
// for whole-hour, negative, large and fractional offsets.
func overviewUsageCases(t *testing.T, random *rand.Rand) []rollupReadCase {
	t.Helper()
	var cases []rollupReadCase
	for _, zone := range []string{"UTC", "Europe/Prague", "America/New_York", "Asia/Tokyo", "Asia/Kolkata"} {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Fatal(err)
		}
		instants := []time.Time{time.Date(2026, 3, 31, 9, 0, 0, 0, time.UTC), time.Date(2026, 10, 27, 23, 0, 0, 0, time.UTC)}
		for range 3 {
			base := []time.Time{time.Date(2026, 3, 27, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)}[random.IntN(2)]
			instants = append(instants, base.Add(time.Duration(random.Int64N(int64(9*24*time.Hour)))))
		}
		for _, now := range instants {
			q := application.OverviewUsageQuery(now, loc)
			cases = append(cases, rollupReadCase{
				name: fmt.Sprintf("overview %s %s", zone, now.Format(time.RFC3339Nano)),
				rollup: func(ctx context.Context, r *rollupRead) (any, error) {
					usage, err := r.overviewUsage(ctx, q)
					return usage, err
				},
				oracle: func(ctx context.Context, tx *sql.Tx) (any, error) {
					usage, err := overviewUsageFromEvents(ctx, tx, q, q.DayStarts[0])
					usage.RollupsUsable = true
					return usage, err
				},
			})
		}
	}
	return cases
}

// The overview's per-key and pool day sums, spend at the same time yesterday
// and error windows from the rollups equal the raw evaluation in every fold
// state: (a) folded, (b) the folder at a random cursor, (c) journal rows
// pending after retention, repricing, moves, deletions and late inserts, and
// (d) the journal applied (R6).
func TestOverviewUsageMatchesRawEventsInEveryFoldState(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	random := rand.New(rand.NewPCG(61, 62))
	seedRollupEquivalence(t, s, random)
	cases := overviewUsageCases(t, random)
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
	// Sanity: the cases see key spend on several days, errors and the
	// same-time-yesterday split.
	tx, err := s.analyticsDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	q := application.OverviewUsageQuery(time.Date(2026, 10, 27, 23, 0, 0, 0, time.UTC), time.UTC)
	usage, err := overviewUsageFromEvents(ctx, tx, q, q.DayStarts[0])
	if err != nil {
		t.Fatal(err)
	}
	days := 0
	for _, day := range usage.Keys["key-0"].Days {
		if day.Requests > 0 {
			days++
		}
	}
	if days < 6 || usage.Pool.Trailing.Errors == 0 || usage.YesterdaySameTime == nil || usage.YesterdaySameTime.Requests == 0 {
		t.Fatalf("fixture does not exercise the overview: %d days, %+v, %+v", days, usage.Pool.Trailing, usage.YesterdaySameTime)
	}
}

// Day sums use UTC-day slice rows with hour corrections for whole-hour zones:
// the six days before yesterday read one day row plus the hours of the zone
// offset each; yesterday, which holds the same-time split, and today read
// hours.
func TestOverviewSlicePlanUsesDayRowsForWholeHourZones(t *testing.T) {
	for _, test := range []struct {
		zone    string
		maxRows int
	}{{"UTC", 6 + 2*24}, {"Europe/Prague", 6*3 + 2*24}, {"Asia/Tokyo", 6*19 + 2*24}} {
		loc, err := time.LoadLocation(test.zone)
		if err != nil {
			t.Fatal(err)
		}
		q := application.OverviewUsageQuery(dashboardNow, loc)
		sums := newOverviewSums(q)
		splits := append(append([]int64{}, sums.bounds...), millis(q.YesterdaySameTime), sums.lastHourFrom, sums.trailingFrom)
		plan := planRollupWindow(millis(q.DayStarts[0]), millis(q.Now), splits)
		sp := planOverviewSlices(plan, sums)
		rows := len(sp.days)
		for _, terms := range sp.hours {
			rows += len(terms)
		}
		// Today is read from hours and yesterday holds the same-time split.
		if len(sp.days) != 6 || rows > test.maxRows {
			t.Errorf("%s: %d day rows, %d terms, want at most %d", test.zone, len(sp.days), rows, test.maxRows)
		}
	}
	kolkata, _ := time.LoadLocation("Asia/Kolkata")
	q := application.OverviewUsageQuery(dashboardNow, kolkata)
	sums := newOverviewSums(q)
	splits := append(append([]int64{}, sums.bounds...), millis(q.YesterdaySameTime), sums.lastHourFrom, sums.trailingFrom)
	if sp := planOverviewSlices(planRollupWindow(millis(q.DayStarts[0]), millis(q.Now), splits), sums); len(sp.days) != 0 {
		t.Fatalf("fractional-offset days must use hour rows: %d day rows", len(sp.days))
	}
}

// While the rollups are unusable, the overview reads only raw events from
// today's local midnight or the trailing error window, whichever is earlier:
// earlier days are zero, yesterday is unknown and today equals the raw
// evaluation (R9).
func TestOverviewUsageFallbackReadsOnlyTheLastDay(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	now := time.Date(2026, 10, 28, 15, 20, 0, 0, time.UTC)
	insert := func(id, key string, at time.Time, cost int64, status, code string) {
		t.Helper()
		if _, err := s.db.Exec(`INSERT INTO usage_events(request_id,api_key_id,model,status,error_code,request_kind,requested_at,
 cost_microdollars) VALUES(?,?,?,?,?,'normal',?,?)`, id, key, "m", status, code, millis(at), cost); err != nil {
			t.Fatal(err)
		}
	}
	insert("old", "key-a", now.Add(-72*time.Hour), 9e9, "success", "")
	insert("yesterday-late", "key-a", now.Add(-17*time.Hour), 5e6, "error", "upstream_error")
	insert("today", "key-a", now.Add(-time.Hour), 2e6, "success", "")
	insert("now", "key-b", now.Add(-time.Minute), 1e6, "error", "client_disconnected")
	if _, err := s.db.Exec(`UPDATE usage_rollup_state SET status='rebuilding' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	q := application.OverviewUsageQuery(now, time.UTC)
	usage, err := s.OverviewUsage(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if usage.RollupsUsable || usage.DaysKnown || usage.YesterdaySameTime != nil {
		t.Fatalf("fallback flags %+v", usage)
	}
	if want := []domain.UsageDay{{}, {}, {}, {}, {}, {}, {}, {Requests: 2, CostUSD: 3}}; !reflect.DeepEqual(usage.Pool.Days, want) {
		t.Fatalf("pool days %+v, want only today", usage.Pool.Days)
	}
	if usage.Keys["key-a"].Days[7].CostUSD != 2 || usage.Keys["key-a"].Days[4].CostUSD != 0 {
		t.Fatalf("key days %+v", usage.Keys["key-a"].Days)
	}
	// The error windows still cover the trailing 24 hours; the disconnect is
	// not an error.
	if usage.Pool.LastHour != (domain.ErrorWindow{Requests: 2, Errors: 0}) || usage.Pool.Trailing != (domain.ErrorWindow{Requests: 1, Errors: 1}) {
		t.Fatalf("error windows %+v %+v", usage.Pool.LastHour, usage.Pool.Trailing)
	}
	service := application.NewOverviewService(s, application.NewShellStatusService(s, nil), func() time.Time { return now })
	response, err := service.Overview(ctx, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if response.Pool.Trend7d != nil || response.Pool.SpendYesterdaySameTimeUSD != nil || response.Pool.SpendTodayUSD != 3 || response.Shell.RollupsUsable {
		t.Fatalf("fallback overview pool %+v", response.Pool)
	}
	for _, row := range response.Groups {
		if row.Trend7d != nil || row.Spend7dUSD != nil {
			t.Fatalf("trends must be null while rollups are unusable: %+v", row)
		}
	}
}
