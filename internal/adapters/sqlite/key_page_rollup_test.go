package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

// keyPageQuery is the key page query of the inclusive local days from..to
// in loc, with the pace window ending at now.
func keyPageQuery(t testing.TB, keyID string, loc *time.Location, from, to string, now time.Time, models []string, recent domain.KeyRequestsQuery) domain.KeyPageUsageQuery {
	t.Helper()
	first, err := time.ParseInLocation("2006-01-02", from, loc)
	if err != nil {
		t.Fatal(err)
	}
	last, err := time.ParseInLocation("2006-01-02", to, loc)
	if err != nil {
		t.Fatal(err)
	}
	var days []time.Time
	for day := first; !day.After(last); day = day.AddDate(0, 0, 1) {
		days = append(days, day.UTC())
	}
	recent.KeyID = keyID
	if recent.Limit == 0 {
		recent.Limit = 10
	}
	return domain.KeyPageUsageQuery{KeyID: keyID, DayStarts: days, End: last.AddDate(0, 0, 1).UTC(),
		PreviousStart: first.AddDate(0, 0, -len(days)).UTC(), PaceStart: now.Add(-domain.KeyPagePaceWindow), Now: now,
		PaceModels: models, Recent: recent}
}

// keyPageCases are key page reads of named, prefixed and unknown keys over
// windows of both event ranges in whole-hour, daylight-saving and
// fractional-offset zones, with pace windows at random instants and recent
// request pages with and without errors only.
func keyPageCases(t *testing.T, random *rand.Rand) []rollupReadCase {
	t.Helper()
	var cases []rollupReadCase
	for _, zone := range []string{"UTC", "Europe/Prague", "Asia/Kolkata"} {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Fatal(err)
		}
		for _, window := range [][2]string{{"2026-03-19", "2026-04-08"}, {"2026-10-02", "2026-11-02"}, {"2026-03-29", "2026-03-29"}} {
			for _, key := range []string{"key-0", "key-1", "\x1fk", "missing"} {
				now := rollupRandomInstant(random)
				models := [][]string{nil, {"model-0", "model-2", "model-9"}}[random.IntN(2)]
				recent := domain.KeyRequestsQuery{Limit: 1 + random.IntN(20), ErrorsOnly: random.IntN(3) == 0}
				if random.IntN(2) == 0 {
					recent.Before = &domain.RequestLogCursor{RequestedAt: rollupRandomInstant(random), RequestID: "req-005000"}
				}
				q := keyPageQuery(t, key, loc, window[0], window[1], now, models, recent)
				cases = append(cases, rollupReadCase{
					name:   fmt.Sprintf("key page %q %s %v", key, zone, window),
					rollup: func(ctx context.Context, r *rollupRead) (any, error) { return r.keyPageUsage(ctx, q) },
					oracle: func(ctx context.Context, tx *sql.Tx) (any, error) {
						usage, err := keyPageUsageRaw(ctx, tx, q)
						usage.RollupsUsable = true
						return usage, err
					},
				})
			}
		}
	}
	if raceDetector {
		// Every zone and fold state stays covered; the full matrix runs
		// without the race detector, which slows SQLite about 40x.
		sampled := cases[:0]
		for i, c := range cases {
			if i%3 == 0 {
				sampled = append(sampled, c)
			}
		}
		cases = sampled
	}
	return cases
}

// The key page's day, model, user-agent, account and error-code rows, its
// latency quantiles, previous period, limit pace and recent requests from
// the rollups equal the raw evaluation of the key's events in every fold
// state: (a) folded, (b) the folder at a random cursor, (c) journal rows
// pending after retention, repricing, moves, deletions and late inserts, and
// (d) the journal applied (R20, R22).
func TestKeyPageUsageMatchesRawEventsInEveryFoldState(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	random := rand.New(rand.NewPCG(71, 72))
	seedRollupEquivalence(t, s, random)
	cases := keyPageCases(t, random)
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
	// Sanity: the fixture exercises every section of the key page.
	prague, _ := time.LoadLocation("Europe/Prague")
	q := keyPageQuery(t, "key-0", prague, "2026-03-19", "2026-04-08", rollupSpringBase.Add(10*24*time.Hour),
		[]string{"model-1"}, domain.KeyRequestsQuery{Limit: 5})
	usage, err := s.KeyPageUsage(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	active := 0
	for _, day := range usage.Days {
		if day.Requests > 0 && day.Latency.TTFTP50 != nil {
			active++
		}
	}
	if !usage.RollupsUsable || active < 10 || len(usage.Models) < 3 || len(usage.UserAgents) < 3 || len(usage.Accounts) < 3 ||
		len(usage.ErrorCodes) < 2 || usage.Previous.Requests == 0 || usage.DetailsFrom == nil || usage.Pace.All.InputTokens == 0 ||
		usage.TotalLatency.TotalP95 == nil || len(usage.Recent.Items) != 5 || usage.Recent.NextCursor == nil {
		t.Fatalf("fixture does not exercise the key page: %d active days, %+v", active, usage)
	}
}

// Key page quantiles from the histograms stay within the relative bound of
// histQuantile (1/64, plus the 0.005 of rounding to two places) of the
// interpolated quantile over the exact values (R22).
func TestKeyPageQuantilesStayWithinTheHistogramBound(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	random := rand.New(rand.NewPCG(73, 74))
	day := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	var ttft, total []float64
	for i := range 3000 {
		first := int64(math.Exp(random.NormFloat64()*1.3 + 6))
		elapsed := first + int64(math.Exp(random.NormFloat64()*1.1+8))
		ttft, total = append(ttft, float64(first)), append(total, float64(elapsed))
		if _, err := s.db.Exec(`INSERT INTO usage_events(request_id,api_key_id,account_id,model,status,request_kind,requested_at,
 output_tokens,total_latency_ms,latency_first_token_ms) VALUES(?,?,?,?,?,'normal',?,?,?,?)`, fmt.Sprintf("q-%d", i), "key-q",
			"acct", "m", "success", millis(day)+random.Int64N(86400000), 100, elapsed, first); err != nil {
			t.Fatal(err)
		}
	}
	foldRollupsTo(t, s, 1700)
	q := keyPageQuery(t, "key-q", time.UTC, "2026-10-14", "2026-10-14", day.Add(30*time.Hour), nil, domain.KeyRequestsQuery{})
	usage, err := s.KeyPageUsage(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		name   string
		got    *float64
		values []float64
		q      float64
	}{
		{"ttft p50", usage.TotalLatency.TTFTP50, ttft, 0.5}, {"ttft p95", usage.TotalLatency.TTFTP95, ttft, 0.95},
		{"total p50", usage.TotalLatency.TotalP50, total, 0.5}, {"total p95", usage.TotalLatency.TotalP95, total, 0.95},
		{"day ttft p95", usage.Days[0].Latency.TTFTP95, ttft, 0.95}, {"day total p95", usage.Days[0].Latency.TotalP95, total, 0.95},
	} {
		want := exactQuantile(check.values, check.q)
		if check.got == nil || math.Abs(*check.got-want) > want/64+0.005 {
			t.Errorf("%s: got %v, exact %.3f", check.name, check.got, want)
		}
	}
}

// The key page executes the same number of statements for 7 and for 90 days
// of a key, with and without filtered pace models (R20).
func TestKeyPageUsageStatementCountIsIndependentOfThePeriod(t *testing.T) {
	ctx := context.Background()
	s, counter := countingStore(t)
	now := time.Date(2026, 10, 28, 10, 37, 0, 0, time.UTC)
	if _, err := s.db.Exec(`WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i+1 FROM n WHERE i<2999)
INSERT INTO usage_events(request_id,api_key_id,account_id,model,status,error_code,request_kind,requested_at,cost_microdollars,
 total_latency_ms,latency_first_token_ms)
SELECT printf('c-%05d',i),'key-c','acct-'||(i%3),'model-'||(i%4),CASE i%9 WHEN 0 THEN 'error' ELSE 'success' END,
 CASE i%9 WHEN 0 THEN 'upstream_error' ELSE '' END,'normal',?-i*2700000,1000+i,900+i,100+i FROM n`, millis(now)); err != nil {
		t.Fatal(err)
	}
	foldRollups(t, s, 1000)
	prague, _ := time.LoadLocation("Europe/Prague")
	var counts []int64
	for _, days := range []int{7, 90} {
		for _, models := range [][]string{nil, {"model-1"}} {
			end := now.In(prague).Format("2006-01-02")
			start := now.In(prague).AddDate(0, 0, 1-days).Format("2006-01-02")
			q := keyPageQuery(t, "key-c", prague, start, end, now, models, domain.KeyRequestsQuery{Limit: 50})
			before := counter.count()
			usage, err := s.KeyPageUsage(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			counts = append(counts, counter.count()-before)
			if usage.Total.Requests == 0 {
				t.Fatalf("%d days: no requests", days)
			}
		}
	}
	t.Logf("key page statements: 7 d %v, 90 d %v", counts[:2], counts[2:])
	if counts[0] != counts[2] || counts[1] != counts[3] {
		t.Fatalf("statement counts differ by period: %v", counts)
	}
}

// Recent requests are read newest first through the key index: cursor pages
// equal the request log's offset pages of the key, including its pending
// entries, and the page query uses idx_usage_events_key_time (R23).
func TestKeyRecentRequestsPageLikeTheRequestLog(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	random := rand.New(rand.NewPCG(75, 76))
	seedRollupEquivalence(t, s, random)
	var cursor *domain.RequestLogCursor
	for offset := 0; ; offset += 7 {
		page, err := s.KeyRecentRequests(ctx, domain.KeyRequestsQuery{KeyID: "key-0", Before: cursor, Limit: 7})
		if err != nil {
			t.Fatal(err)
		}
		want, err := s.ListRequestLogs(ctx, domain.RequestLogFilter{Limit: 7, Offset: offset, APIKeyIDs: []string{"key-0"}})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(page.Items, want.Requests) {
			t.Fatalf("offset %d: key page rows differ from the request log:\n got %+v\nwant %+v", offset, page.Items, want.Requests)
		}
		if page.NextCursor == nil {
			if want.HasMore {
				t.Fatalf("offset %d: no cursor but the request log has more", offset)
			}
			break
		}
		next, err := domain.ParseRequestLogCursor(*page.NextCursor)
		if err != nil {
			t.Fatal(err)
		}
		cursor = &next
		if offset > 3000 {
			t.Fatal("paging does not end")
		}
	}
	newest, args := keyNewestEvents(domain.KeyRequestsQuery{KeyID: "key-0", Before: cursor, Limit: 7}, 8)
	plan := rollupRows(t, s.readDB, `EXPLAIN QUERY PLAN `+newest, args...)
	if joined := strings.Join(plan, "\n"); !strings.Contains(joined, "idx_usage_events_key_time") {
		t.Fatalf("recent requests do not use the key index:\n%s", joined)
	}
}

// Errors only examines at most 5000 of the key's rows per call: a failed
// request behind 11 000 successful ones is found on the third call, after
// two empty pages whose cursors continue after the last examined row (R23).
func TestKeyFailedRequestsStopAtTheScanBound(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	if _, err := s.db.Exec(`WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i+1 FROM n WHERE i<10999)
INSERT INTO usage_events(request_id,api_key_id,model,status,request_kind,requested_at)
SELECT printf('ok-%05d',i),'key-e','m','success','normal',?-i*1000 FROM n`, millis(now)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO usage_events(request_id,api_key_id,model,status,error_code,request_kind,requested_at)
 VALUES('failed','key-e','m','error','upstream_error','normal',?),('other-key','key-x','m','error','','normal',?)`,
		millis(now.Add(-time.Hour*4)), millis(now)); err != nil {
		t.Fatal(err)
	}
	q := domain.KeyRequestsQuery{KeyID: "key-e", Limit: 50, ErrorsOnly: true}
	var calls []int
	for range 4 {
		page, err := s.KeyRecentRequests(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		calls = append(calls, len(page.Items))
		if len(page.Items) == 1 {
			if page.Items[0].RequestID != "failed" || page.NextCursor != nil {
				t.Fatalf("found %+v, cursor %v", page.Items[0], page.NextCursor)
			}
			break
		}
		if page.NextCursor == nil {
			t.Fatalf("call %d: no cursor before the failed request", len(calls))
		}
		cursor, err := domain.ParseRequestLogCursor(*page.NextCursor)
		if err != nil {
			t.Fatal(err)
		}
		if want := now.Add(-time.Duration(len(calls)*domain.KeyErrorsScanLimit-1) * time.Second); !cursor.RequestedAt.Equal(want) {
			t.Fatalf("call %d: cursor at %s, want the last examined row at %s", len(calls), cursor.RequestedAt, want)
		}
		q.Before = &cursor
	}
	if !reflect.DeepEqual(calls, []int{0, 0, 1}) {
		t.Fatalf("errors-only calls returned %v rows", calls)
	}
}
