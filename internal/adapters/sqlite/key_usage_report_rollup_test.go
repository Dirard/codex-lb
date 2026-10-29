package sqlite

import (
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
	"testing"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

// seedKeyUsageGroups adds groups and keys to the equivalence fixture: key-0
// in group-a, key "\x1fk" (whose dimension needs escaping) in group-b, key-1
// and the idle key-2 without a group.
func seedKeyUsageGroups(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	for _, group := range []domain.AccountGroup{{ID: "group-a", Name: "Alpha"}, {ID: "group-b", Name: "Beta"}} {
		if err := s.SaveGroup(ctx, group, fixedTime); err != nil {
			t.Fatal(err)
		}
	}
	groupA, groupB := "group-a", "group-b"
	for _, key := range []domain.APIKey{testKey("\x1fk", &groupB), testKey("key-2", nil)} {
		if err := s.SaveAPIKey(ctx, key, fixedTime); err != nil {
			t.Fatal(err)
		}
	}
	moveKeyToGroup(t, s, "key-0", &groupA)
}

func moveKeyToGroup(t *testing.T, s *Store, key string, group *string) {
	t.Helper()
	if _, err := s.db.Exec(`UPDATE api_keys SET group_id=? WHERE id=?`, group, key); err != nil {
		t.Fatal(err)
	}
}

// keyUsageSelections are the selections of the equivalence cases: every key,
// one group, the ungrouped keys, explicit keys with an unknown id, several
// groups with the ungrouped keys and an unknown group.
var keyUsageSelections = []domain.KeySelection{
	{},
	{GroupIDs: []string{"group-a"}},
	{Ungrouped: true},
	{KeyIDs: []string{"key-1", "missing"}},
	{GroupIDs: []string{"group-a", "group-b"}, Ungrouped: true},
	{GroupIDs: []string{"missing"}},
}

func keyUsageQuery(t testing.TB, start, end string, loc *time.Location, selection domain.KeySelection, now time.Time) domain.KeyUsageQuery {
	t.Helper()
	q, err := application.NewKeyUsageReportService(nil, func() time.Time { return now }).Query(application.KeyUsageReportParams{
		StartDate: start, EndDate: end, Location: loc, GroupIDs: selection.GroupIDs, KeyIDs: selection.KeyIDs,
		Ungrouped: selection.Ungrouped})
	if err != nil {
		t.Fatal(err)
	}
	return q
}

// keyUsageRaw is the raw evaluation of KeyUsageReport in a snapshot.
func keyUsageRaw(ctx context.Context, tx *sql.Tx, q domain.KeyUsageQuery) (domain.KeyUsageData, error) {
	sums, err := newKeyUsageSums(ctx, tx, q)
	if err != nil {
		return domain.KeyUsageData{}, err
	}
	if err := sums.readEvents(ctx, tx); err != nil {
		return domain.KeyUsageData{}, err
	}
	return sums.result(false), nil
}

// keyUsageCases are reports over both fixture ranges, a daylight-saving week
// and a window whose previous period covers folded legacy hours, for
// whole-hour, fractional and half-hour-DST zones and every selection.
func keyUsageCases(t *testing.T) []rollupReadCase {
	t.Helper()
	var cases []rollupReadCase
	now := time.Date(2026, 11, 3, 10, 37, 0, 0, time.UTC)
	for _, zone := range rollupEquivalenceZones {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Fatal(err)
		}
		for _, window := range [][2]string{{"2026-03-19", "2026-04-08"}, {"2026-10-02", "2026-11-03"}, {"2026-03-25", "2026-03-31"}} {
			for _, selection := range keyUsageSelections {
				q := keyUsageQuery(t, window[0], window[1], loc, selection, now)
				cases = append(cases, rollupReadCase{
					name: fmt.Sprintf("key usage %s %s %+v", zone, window, selection),
					rollup: func(ctx context.Context, r *rollupRead) (any, error) {
						sums, err := newKeyUsageSums(ctx, r.tx, q)
						if err != nil {
							return nil, err
						}
						err = r.keyUsage(ctx, sums)
						return sums.result(true), err
					},
					oracle: func(ctx context.Context, tx *sql.Tx) (any, error) {
						data, err := keyUsageRaw(ctx, tx, q)
						data.RollupsUsable = true
						return data, err
					},
				})
			}
		}
	}
	if raceDetector {
		sampled := cases[:0]
		for i, c := range cases {
			if i%5 == 0 {
				sampled = append(sampled, c)
			}
		}
		cases = sampled
	}
	return cases
}

// Per-key, per-day and previous-period measures of every selection from the
// rollups equal the raw evaluation in every fold state, with folded legacy
// hours, an escaped key dimension, a key moved between groups and the
// ungrouped selection (R30).
func TestKeyUsageReportMatchesRawEventsInEveryFoldState(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	random := rand.New(rand.NewPCG(71, 72))
	seedRollupEquivalence(t, s, random)
	seedKeyUsageGroups(t, s)
	cases := keyUsageCases(t)
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
		if round == 0 {
			// The key moves: every day of it counts in its new group.
			groupB := "group-b"
			moveKeyToGroup(t, s, "key-0", &groupB)
		}
		assertRollupReadsMatchRaw(t, s, fmt.Sprintf("(c) journal after round %d", round), cases)
		foldRollups(t, s, 1000)
		assertRollupReadsMatchRaw(t, s, fmt.Sprintf("(d) applied round %d", round), cases)
	}
	// Sanity: the cases see usage on many days of several keys, legacy
	// hours in a previous period and the moved key in group-b.
	prague, _ := time.LoadLocation("Europe/Prague")
	data, err := s.KeyUsageReport(ctx, keyUsageQuery(t, "2026-03-19", "2026-04-08", prague, domain.KeySelection{GroupIDs: []string{"group-b"}}, fixedTime))
	if err != nil {
		t.Fatal(err)
	}
	if !data.RollupsUsable || len(data.Keys) != 2 || len(data.Days["key-0"]) != 21 || data.Previous.Requests == 0 {
		t.Fatalf("fixture does not exercise the report: %d keys, %d days, previous %+v", len(data.Keys), len(data.Days["key-0"]), data.Previous)
	}
	active := 0
	for _, day := range data.Days["key-0"] {
		if day.Requests > 0 {
			active++
		}
	}
	if active < 15 {
		t.Fatalf("key-0 has usage on %d days", active)
	}
}

// The raw evaluation attributes events and legacy hours to local days exactly
// as the admin report filtered by one key does: an independent oracle of the
// day split in a daylight-saving week (R30).
func TestKeyUsageDaysMatchTheAdminReportPerKey(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	random := rand.New(rand.NewPCG(73, 74))
	seedRollupEquivalence(t, s, random)
	seedKeyUsageGroups(t, s)
	foldRollups(t, s, 1000)
	for _, zone := range []string{"Europe/Prague", "Asia/Kolkata"} {
		loc, _ := time.LoadLocation(zone)
		q := keyUsageQuery(t, "2026-03-20", "2026-04-02", loc, domain.KeySelection{}, fixedTime)
		data, err := s.KeyUsageReport(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range data.Keys {
			report, err := s.QueryReports(ctx, domain.ReportFilter{Start: q.DayStarts[0], End: q.End,
				PreviousStart: q.PreviousStart, Location: loc, APIKeyIDs: []string{key.ID}})
			if err != nil {
				t.Fatal(err)
			}
			days := data.Days[key.ID]
			for i, row := range report.Daily {
				var day domain.UsageSums
				if days != nil {
					day = days[i]
				}
				reasoning := day.ReasoningKnownTokens()
				if day.Requests != row.Requests || day.InputTokens != row.InputTokens || day.OutputTokens != row.OutputTokens ||
					day.CachedInputTokens != row.CachedInputTokens || day.Errors != row.ErrorCount || day.Cancelled != row.CancelledCount ||
					(reasoning == nil) != (row.ReasoningTokens == nil) || reasoning != nil && *reasoning != *row.ReasoningTokens ||
					math.Abs(day.CostUSD-row.CostUSD) > 0.00005 {
					t.Fatalf("%s %s %s: key usage %+v, admin report %+v", zone, key.ID, row.Date, day, row)
				}
			}
		}
	}
}

// Whole-hour zones read one UTC-day row and the offset's hour rows per day;
// fractional zones correct the day row around their raw split hours.
func TestKeyUsageSlicePlanUsesDayRows(t *testing.T) {
	for _, test := range []struct {
		zone     string
		maxTerms int
	}{{"UTC", 90}, {"Europe/Prague", 90 * 5}, {"Asia/Kolkata", 90 * 12}, {"Asia/Tokyo", 90 * 19}} {
		loc, err := time.LoadLocation(test.zone)
		if err != nil {
			t.Fatal(err)
		}
		q := keyUsageQuery(t, "2026-08-01", "2026-10-29", loc, domain.KeySelection{}, dashboardNow)
		sums := &keyUsageSums{q: q, prevFrom: millis(q.PreviousStart), prevTo: millis(q.DayStarts[0])}
		for _, start := range q.DayStarts {
			sums.bounds = append(sums.bounds, millis(start))
		}
		sums.bounds = append(sums.bounds, millis(q.End))
		plan := planRollupWindow(sums.prevFrom, millis(q.End), append(append([]int64{}, sums.bounds...), sums.prevFrom))
		b := planKeyUsageSlices(plan, sums)
		current, previous := 0, 0
		for _, buckets := range []map[int64][]sliceTerm{b.days, b.hours} {
			for _, terms := range buckets {
				for _, term := range terms {
					if term.day < 0 {
						previous++
					} else {
						current++
					}
				}
			}
		}
		dayRows := 0
		for _, terms := range b.days {
			for _, term := range terms {
				if term.day >= 0 {
					dayRows++
				}
			}
		}
		if len(q.DayStarts) != 90 || dayRows != 90 || current > test.maxTerms || previous > 90+48 {
			t.Errorf("%s: %d day rows, %d current terms (max %d), %d previous terms", test.zone, dayRows, current, test.maxTerms, previous)
		}
	}
}

// A selection whose keys times days exceed the bound is refused before any
// usage is read; a strict selection without keys reads nothing.
func TestKeyUsageReportBounds(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	for i := range domain.KeyUsageMaxKeyDays/domain.KeyUsageMaxDays + 1 {
		if err := s.SaveAPIKey(ctx, testKey(fmt.Sprintf("key-%04d", i), nil), fixedTime); err != nil {
			t.Fatal(err)
		}
	}
	q := keyUsageQuery(t, "2024-10-31", "2026-10-31", time.UTC, domain.KeySelection{}, fixedTime)
	if _, err := s.KeyUsageReport(ctx, q); !errors.Is(err, domain.ErrReportTooLarge) {
		t.Fatalf("731 days of %d keys: %v", domain.KeyUsageMaxKeyDays/domain.KeyUsageMaxDays+1, err)
	}
	q.Selection = domain.KeySelection{GroupIDs: []string{"missing"}}
	data, err := s.KeyUsageReport(ctx, q)
	if err != nil || len(data.Keys) != 0 || len(data.Days) != 0 || data.Previous != (domain.UsageSums{}) {
		t.Fatalf("empty selection %+v %v", data, err)
	}
	q.DayStarts = append(q.DayStarts, q.End)
	if _, err := s.KeyUsageReport(ctx, q); !errors.Is(err, ErrInvalid) {
		t.Fatalf("732 days: %v", err)
	}
}

// keyUsageReportAt runs the report use case on a store at dashboardNow.
func keyUsageReportAt(t testing.TB, s *Store, p application.KeyUsageReportParams) domain.KeyUsageReport {
	t.Helper()
	report, err := application.NewKeyUsageReportService(s, func() time.Time { return dashboardNow }).Report(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	return report
}

// keyUsageExportAt runs the per-day export on a store at dashboardNow and
// returns its rows.
func keyUsageExportAt(t testing.TB, s *Store, p application.KeyUsageReportParams) int {
	t.Helper()
	export, err := application.NewKeyUsageReportService(s, func() time.Time { return dashboardNow }).Export(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	rows := 0
	for range export.Rows() {
		rows++
	}
	return rows
}

// The report and the export execute the same number of statements for 30
// keys, 5 groups and 10 accounts as for 300 keys, 50 groups and 100
// accounts, and for 7 as for 90 days (R30).
func TestKeyUsageReportStatementCountIsIndependentOfScale(t *testing.T) {
	prague, _ := time.LoadLocation("Europe/Prague")
	kolkata, _ := time.LoadLocation("Asia/Kolkata")
	counts := map[string][]int64{}
	for _, scale := range []dashboardScale{{keys: 30, groups: 5, accounts: 10, days: 2, eventsPerKeyHour: 1},
		{keys: 300, groups: 50, accounts: 100, days: 2, eventsPerKeyHour: 1}} {
		s, counter := countingStore(t)
		seedDashboardScale(t, s, scale)
		measure := func(name string, read func()) {
			before := counter.count()
			read()
			counts[name] = append(counts[name], counter.count()-before)
		}
		for _, days := range []int{7, 90} {
			start := dashboardNow.AddDate(0, 0, 1-days).Format(time.DateOnly)
			measure("prague all keys", func() {
				report := keyUsageReportAt(t, s, application.KeyUsageReportParams{StartDate: start, Location: prague})
				if report.Selection.ResolvedKeyCount != scale.keys || report.ActiveKeys != scale.keys || report.SeriesBy != domain.SeriesGroup {
					t.Fatalf("report of %d keys: %+v, %d active", scale.keys, report.Selection, report.ActiveKeys)
				}
			})
			measure("prague groups", func() {
				report := keyUsageReportAt(t, s, application.KeyUsageReportParams{StartDate: start, Location: prague,
					GroupIDs: []string{"grp-1", "grp-2"}, KeyIDs: []string{"key-0"}})
				if report.Selection.ResolvedKeyCount == 0 || len(report.Groups) != 3 {
					t.Fatalf("group report %+v %d groups", report.Selection, len(report.Groups))
				}
			})
			measure("kolkata all keys", func() {
				keyUsageReportAt(t, s, application.KeyUsageReportParams{StartDate: start, Location: kolkata})
			})
			measure("prague export", func() {
				if rows := keyUsageExportAt(t, s, application.KeyUsageReportParams{StartDate: start, Location: prague}); rows < scale.keys {
					t.Fatalf("%d export rows", rows)
				}
			})
		}
	}
	for name, n := range counts {
		t.Logf("%s: %v statements", name, n)
		for _, count := range n {
			if count != n[0] || count == 0 {
				t.Errorf("%s: statement counts %v differ with keys or days", name, n)
				break
			}
		}
	}
}

// The report answers within the 300 ms page budget, warm, for 300 keys and
// 50 groups with every key active in every hour of the days it reads (R49).
// The race detector slows SQLite about 40-fold, so it only checks the shape
// there.
func TestKeyUsageReportMeetsItsLatencyBudgetAtTargetScale(t *testing.T) {
	scale := dashboardScale{keys: 300, groups: 50, accounts: 100, days: 8, eventsPerKeyHour: 1}
	if raceDetector {
		scale.days = 1
	}
	s, _ := testStore(t)
	seedDashboardScale(t, s, scale)
	prague, _ := time.LoadLocation("Europe/Prague")
	params := application.KeyUsageReportParams{StartDate: dashboardNow.AddDate(0, 0, -29).Format(time.DateOnly), Location: prague}
	report := keyUsageReportAt(t, s, params)
	if report.ActiveKeys != scale.keys || len(report.Daily) != 30 || len(report.Series) != 9 {
		t.Fatalf("unexpected report: %d active keys, %d days, %d series", report.ActiveKeys, len(report.Daily), len(report.Series))
	}
	var runs []time.Duration
	for range 5 {
		start := time.Now()
		keyUsageReportAt(t, s, params)
		runs = append(runs, time.Since(start))
	}
	slices.Sort(runs)
	t.Logf("30-day key report at 300 keys / 50 groups, %d events: median %v (min %v, max %v)",
		scale.keys*scale.days*24*scale.eventsPerKeyHour, runs[2], runs[0], runs[4])
	if !raceDetector && runs[2] > 300*time.Millisecond {
		t.Fatalf("key report median %v exceeds the 300 ms budget", runs[2])
	}
}

// benchmarkKeyUsageReports adds the reports for API-key users to
// BenchmarkDashboardPages: every key and three groups for 30 and 90 days, and
// the 90-day per-day export encoded as CSV, in a whole-hour, a
// daylight-saving and a fractional-offset zone.
func benchmarkKeyUsageReports(b *testing.B, s *Store) {
	for _, zone := range []string{"UTC", "Europe/Prague", "Asia/Kolkata"} {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			b.Fatal(err)
		}
		for _, days := range []int{30, 90} {
			start := dashboardNow.AddDate(0, 0, 1-days).Format(time.DateOnly)
			b.Run(fmt.Sprintf("key_report_all_%dd_%s", days, zone), func(b *testing.B) {
				for range b.N {
					keyUsageReportAt(b, s, application.KeyUsageReportParams{StartDate: start, Location: loc})
				}
			})
			b.Run(fmt.Sprintf("key_report_3groups_%dd_%s", days, zone), func(b *testing.B) {
				for range b.N {
					keyUsageReportAt(b, s, application.KeyUsageReportParams{StartDate: start, Location: loc,
						GroupIDs: []string{"grp-1", "grp-2", "grp-3"}})
				}
			})
		}
		b.Run("key_report_csv_90d_"+zone, func(b *testing.B) {
			params := application.KeyUsageReportParams{StartDate: dashboardNow.AddDate(0, 0, -89).Format(time.DateOnly), Location: loc}
			service := application.NewKeyUsageReportService(s, func() time.Time { return dashboardNow })
			for range b.N {
				export, err := service.Export(context.Background(), params)
				if err != nil {
					b.Fatal(err)
				}
				out := csv.NewWriter(io.Discard)
				for row := range export.Rows() {
					if err := out.Write([]string{row.Date, row.GroupName, row.KeyID, row.KeyName, strconv.FormatInt(row.Usage.Requests, 10),
						strconv.FormatInt(row.Usage.InputTokens, 10), strconv.FormatFloat(row.Usage.CostUSD, 'f', 6, 64)}); err != nil {
						b.Fatal(err)
					}
				}
				out.Flush()
			}
		})
	}
}
