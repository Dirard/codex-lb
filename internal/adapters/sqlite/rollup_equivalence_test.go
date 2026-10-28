package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

// Two event ranges cover the daylight-saving transitions of the tested
// zones: Prague 2026-03-29 and 10-25, Lord Howe 04-05 and 10-04, New York
// 11-01.
var (
	rollupSpringBase = time.Date(2026, 3, 18, 0, 0, 0, 0, time.UTC)
	rollupAutumnBase = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
)

var rollupEquivalenceZones = []string{"UTC", "Europe/Prague", "America/New_York", "Asia/Kolkata", "Asia/Kathmandu", "Australia/Lord_Howe"}

// seedRollupEquivalence fills a store with random events of every dimension
// edge case in both ranges, late events, folded legacy hours, conversations
// and errors on both sides of the import watermark, accounts with aliases
// and named keys.
func seedRollupEquivalence(t *testing.T, s *Store, random *rand.Rand) {
	t.Helper()
	ctx := context.Background()
	for i, alias := range []string{"alpha", "", "gamma"} {
		id := fmt.Sprintf("acct-%d", i)
		if err := s.SaveAccount(ctx, domain.Account{ID: id, Kind: domain.AccountChatGPT, Email: id + "@example.invalid",
			Alias: alias, PlanType: "plus", Status: domain.AccountActive, CreatedAt: fixedTime}); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"key-0", "key-1"} {
		if err := s.SaveAPIKey(ctx, testKey(id, nil), fixedTime); err != nil {
			t.Fatal(err)
		}
	}
	// Account history deletion subtracts discarded events from the totals.
	for _, scope := range [][2]string{{"all", "all"}, {"key", "key-0"}, {"key", "key-1"}, {"key", "\x1fk"}, {"key", ""}} {
		if _, err := s.db.Exec(`INSERT INTO usage_totals(scope,scope_id,request_count,failed_count,input_tokens,output_tokens,
 cached_input_tokens,reasoning_tokens,cost_microdollars) VALUES(?,?,1e9,1e9,1e12,1e12,1e12,1e12,1e15)`, scope[0], scope[1]); err != nil {
			t.Fatal(err)
		}
	}
	for i, account := range []string{"acct-0", "", "acct-2"} {
		if _, err := s.db.Exec(`INSERT INTO usage_reservations(id,api_key_id,account_id,model,status,created_at,updated_at,
 needs_reconciliation) VALUES(?,'key-0',?,'model-pending','reserved',?,?,1)`, fmt.Sprintf("pending-%d", i), account,
			millis(rollupSpringBase.Add(time.Duration(i)*72*time.Hour)), millis(rollupSpringBase)); err != nil {
			t.Fatal(err)
		}
	}
	setRollupWatermark(t, s, rollupSpringBase.Add(3*24*time.Hour+30*time.Minute))
	insertRollupFixtureAt(t, s, random, 0, 2200, rollupSpringBase, 22*24*time.Hour)
	insertRollupFixtureAt(t, s, random, 2200, 2200, rollupAutumnBase, 34*24*time.Hour)
	for i := range 500 {
		base := []time.Time{rollupSpringBase.Add(-40 * 24 * time.Hour), rollupSpringBase, rollupAutumnBase}[i%3]
		bucket := base.Unix() + random.Int64N(30*24)*3600
		if _, err := s.db.Exec(`INSERT OR IGNORE INTO legacy_hourly_usage(bucket_epoch,account_id,api_key_id,model,
 service_tier,request_kind,is_deleted,request_count,error_count,cancelled_count,input_tokens,output_tokens,
 reasoning_tokens,output_or_reasoning_tokens,cached_input_tokens,cached_input_tokens_clamped,cost_usd,cost_count,
 reasoning_known_count) VALUES(?,?,?,?,'',?,?,?,?,?,?,?,?,?,?,?,?,1,?)`,
			bucket, []string{"acct-0", "\x1f", "acct-9", "\x1f\x1fodd"}[i%4], []string{"key-0", "key-1", "\x1f", "\x1f\x1fk"}[random.IntN(4)],
			fmt.Sprintf("model-%d", i%4), []string{"normal", "warmup"}[i%7/6], i%5/4, 1+random.Int64N(20), random.Int64N(3),
			random.Int64N(2), random.Int64N(9000), random.Int64N(900), random.Int64N(90), random.Int64N(900),
			random.Int64N(9000), random.Int64N(9000), float64(random.Int64N(5000000))/1e6, random.Int64N(3)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(`INSERT OR IGNORE INTO legacy_conversation_hourly(bucket_epoch,conversation_id,account_id,is_deleted,request_count)
 VALUES(?,?,?,0,?)`, bucket, []string{"conv-a", "conv-legacy", fmt.Sprintf("conv-%d", i%30)}[i%3], []string{"acct-0", "\x1f"}[i%2],
			1+random.Int64N(4)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(`INSERT OR IGNORE INTO legacy_hourly_errors(bucket_epoch,account_id,error_code,error_count)
 VALUES(?,?,?,?)`, bucket, "acct-0", []string{"upstream_error", "quota", "client_disconnected"}[i%3], 1+random.Int64N(9)); err != nil {
			t.Fatal(err)
		}
	}
}

// rollupReadCase is one analytics read through both paths.
type rollupReadCase struct {
	name string
	// raw marks a filter the rollups always hand to the raw path.
	raw    bool
	rollup func(ctx context.Context, r *rollupRead) (any, error)
	oracle func(ctx context.Context, tx *sql.Tx) (any, error)
}

// assertRollupReadsMatchRaw evaluates every case through the rollup and the
// raw path in one read snapshot and requires identical results. It returns
// how many reads the rollups declined.
func assertRollupReadsMatchRaw(t *testing.T, s *Store, state string, cases []rollupReadCase) int {
	t.Helper()
	ctx := context.Background()
	tx, err := s.analyticsDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	reader, err := openRollupRead(ctx, tx)
	if err != nil || reader == nil {
		t.Fatalf("%s: rollups unusable: %v", state, err)
	}
	declined := 0
	for _, c := range cases {
		got, err := c.rollup(ctx, reader)
		if declines := errors.Is(err, errRollupDeclined); declines || c.raw {
			if !declines {
				t.Fatalf("%s %s: the rollups served a raw-only read", state, c.name)
			}
			if !c.raw {
				declined++
			}
			continue
		}
		want, wantErr := c.oracle(ctx, tx)
		if (err == nil) != (wantErr == nil) || err != nil && !errors.Is(err, wantErr) && err.Error() != wantErr.Error() {
			t.Fatalf("%s %s: rollup error %v, raw error %v", state, c.name, err, wantErr)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s %s: rollup path differs from raw events:\n got %+v\nwant %+v", state, c.name, got, want)
		}
	}
	return declined
}

// rollupReportCases are reports over both ranges for every zone and the
// filter matrix of the reference test, plus key reports.
func rollupReportCases(t *testing.T) []rollupReadCase {
	t.Helper()
	filters := []domain.ReportFilter{
		{},
		{AccountIDs: []string{"acct-0", "\x1fodd"}},
		{AccountIDs: []string{""}},
		{Model: "model-1"},
		{UserAgentGroup: "codex"},
		{UserAgentGroup: "Missing User-Agent"},
		{UserAgentGroup: "\x1fua"},
		{APIKeyIDs: []string{"key-1"}},
		{APIKeyIDs: []string{"key-0", "\x1fk"}, Model: "model-2"},
		{AccountIDs: []string{"acct-1"}, UserAgentGroup: "sdk"},
	}
	var cases []rollupReadCase
	for _, zone := range rollupEquivalenceZones {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Fatal(err)
		}
		for _, window := range [][2]string{{"2026-03-19", "2026-04-08"}, {"2026-10-02", "2026-11-02"}, {"2026-03-25", "2026-03-31"}} {
			startDay, _ := time.ParseInLocation("2006-01-02", window[0], loc)
			endDay, _ := time.ParseInLocation("2006-01-02", window[1], loc)
			days := 0
			for day := startDay; !day.After(endDay); day = day.AddDate(0, 0, 1) {
				days++
			}
			for _, filter := range filters {
				filter.Start, filter.End, filter.Location = startDay.UTC(), endDay.AddDate(0, 0, 1).UTC(), loc
				filter.PreviousStart = startDay.AddDate(0, 0, -days).UTC()
				cases = append(cases, rollupReadCase{
					name: fmt.Sprintf("report %s %s %+v", zone, window, filter),
					rollup: func(ctx context.Context, r *rollupRead) (any, error) {
						return r.queryReports(ctx, filter)
					},
					oracle: func(ctx context.Context, tx *sql.Tx) (any, error) { return queryReportsRaw(ctx, tx, filter) },
				})
			}
		}
	}
	return cases
}

// rollupEquivalenceCases are every reader's cases at instant now.
func rollupEquivalenceCases(t *testing.T, random *rand.Rand) []rollupReadCase {
	t.Helper()
	cases := rollupReportCases(t)
	cases = append(cases, rollupDashboardCases(random)...)
	cases = append(cases, rollupKeyCases(random)...)
	cases = append(cases, rollupRequestLogCases(random)...)
	cases = append(cases, rollupConversationCases(random)...)
	if raceDetector {
		// Every reader and fold state stays covered; the full matrix runs
		// without the race detector, which slows SQLite about 40x.
		sampled := cases[:0]
		for i, c := range cases {
			if i%4 == 0 {
				sampled = append(sampled, c)
			}
		}
		cases = sampled
	}
	return cases
}

// rollupConversationCases are conversation lists for timeframes, searches
// and pages, and conversation details of present, blank-padded, folded-only
// and unknown ids.
func rollupConversationCases(random *rand.Rand) []rollupReadCase {
	var cases []rollupReadCase
	for _, search := range []string{"", "conv", "a", "conv-a", "%", "_", " conv"} {
		for _, page := range [][2]int{{5, 0}, {3, 2}, {1000, 0}, {2, 100}} {
			var since time.Time
			if page[1] == 2 {
				since = rollupRandomInstant(random).Add(-7 * 24 * time.Hour)
			} else if page[0] == 5 {
				since = rollupRandomInstant(random).Add(-30 * 24 * time.Hour)
			}
			filter := domain.ConversationFilter{Since: since, Search: search, Limit: page[0], Offset: page[1]}
			cases = append(cases, rollupReadCase{
				name:   fmt.Sprintf("conversations %+v", filter),
				raw:    search != strings.Trim(search, " "),
				rollup: func(ctx context.Context, r *rollupRead) (any, error) { return r.listConversations(ctx, filter) },
				oracle: func(ctx context.Context, tx *sql.Tx) (any, error) { return listConversationsRaw(ctx, tx, filter) },
			})
		}
	}
	for _, id := range []string{"conv-a", "conv-b", "conv-c", "conv-legacy", " conv-a ", "missing"} {
		cases = append(cases, rollupReadCase{
			name:   fmt.Sprintf("conversation %q", id),
			rollup: func(ctx context.Context, r *rollupRead) (any, error) { return r.conversationDetails(ctx, id) },
			oracle: func(ctx context.Context, tx *sql.Tx) (any, error) { return conversationDetailsRaw(ctx, tx, id) },
		})
	}
	return cases
}

// rollupRequestLogCases are request-log facets and totals without bounds,
// for timeframes and every filter the log cube serves; search and
// conversation filters are declined.
func rollupRequestLogCases(random *rand.Rand) []rollupReadCase {
	var cases []rollupReadCase
	at := func() *time.Time { value := rollupRandomInstant(random); return &value }
	filters := []domain.RequestLogFilter{
		{},
		{AccountIDs: []string{"acct-0", ""}},
		{AccountIDs: []string{"\x1fodd"}, Statuses: []string{"error", "cancelled"}},
		{APIKeyIDs: []string{"key-1", "\x1fk"}},
		{Models: []string{"model-1"}, ReasoningEfforts: []string{"low", ""}},
		{ModelOptions: []string{"model-0:::high", "model-2", "model-1:::", ":::x"}},
		{ModelOptions: []string{":::x"}},
		{Statuses: []string{"success"}},
		{Search: "conv"},
		{ConversationID: "conv-a"},
	}
	for i, filter := range filters {
		for _, bounds := range []int{0, 1, 2, 3} {
			filter := filter
			switch bounds {
			case 1:
				since := at()
				filter.Since = since
			case 2:
				until := at()
				since := until.Add(-24 * time.Hour)
				filter.Since, filter.Until = &since, until
			case 3:
				filter.Until = at()
			}
			if i > 0 && bounds == 3 {
				continue
			}
			options := filter
			options.Statuses = nil
			cases = append(cases, rollupReadCase{
				name:   fmt.Sprintf("request-log options %d/%d", i, bounds),
				raw:    !requestLogCubeEligible(filter),
				rollup: func(ctx context.Context, r *rollupRead) (any, error) { return r.requestLogOptions(ctx, options) },
				oracle: func(ctx context.Context, tx *sql.Tx) (any, error) { return requestLogOptionsRaw(ctx, tx, options) },
			}, rollupReadCase{
				name:   fmt.Sprintf("request-log total %d/%d", i, bounds),
				raw:    !requestLogCubeEligible(filter),
				rollup: func(ctx context.Context, r *rollupRead) (any, error) { return r.requestLogTotal(ctx, filter) },
				oracle: func(ctx context.Context, tx *sql.Tx) (any, error) { return requestLogTotalRaw(ctx, tx, filter) },
			})
		}
	}
	return cases
}

// rollupKeyCases are key usage and trends of named, prefixed and unknown keys
// over seven days at random instants.
func rollupKeyCases(random *rand.Rand) []rollupReadCase {
	var cases []rollupReadCase
	for _, key := range []string{"key-0", "key-1", "\x1fk", "missing"} {
		for range 3 {
			until := rollupRandomInstant(random)
			since := until.Add(-7 * 24 * time.Hour)
			cases = append(cases, rollupReadCase{
				name:   fmt.Sprintf("key usage %q %s", key, until),
				rollup: func(ctx context.Context, r *rollupRead) (any, error) { return r.keyUsage7Day(ctx, key, since, until) },
				oracle: func(ctx context.Context, tx *sql.Tx) (any, error) { return keyUsage7DayRaw(ctx, tx, key, since, until) },
			}, rollupReadCase{
				name: fmt.Sprintf("key trends %q %s", key, until),
				rollup: func(ctx context.Context, r *rollupRead) (any, error) {
					return r.keyTrendBuckets(ctx, key, since, until)
				},
				oracle: func(ctx context.Context, tx *sql.Tx) (any, error) {
					return keyTrendBucketsRaw(ctx, tx, key, since, until)
				},
			})
		}
	}
	return cases
}

// rollupRandomInstant is a random instant with milliseconds inside the
// event ranges.
func rollupRandomInstant(random *rand.Rand) time.Time {
	base := []time.Time{rollupSpringBase, rollupAutumnBase}[random.IntN(2)]
	return base.Add(time.Duration(random.Int64N(int64(30 * 24 * time.Hour))))
}

// rollupDashboardCases are the overview timeframes at random instants, plus
// trend buckets that do not fall on hours.
func rollupDashboardCases(random *rand.Rand) []rollupReadCase {
	var cases []rollupReadCase
	for _, frame := range []struct {
		window        time.Duration
		size, buckets int
	}{{24 * time.Hour, 3600, 24}, {7 * 24 * time.Hour, 21600, 28}, {30 * 24 * time.Hour, 86400, 30}, {24 * time.Hour, 5400, 16}} {
		for range 4 {
			end := rollupRandomInstant(random)
			start := end.Add(-frame.window)
			cases = append(cases, rollupReadCase{
				name: fmt.Sprintf("dashboard %s-%s by %d", start, end, frame.size),
				rollup: func(ctx context.Context, r *rollupRead) (any, error) {
					return r.dashboardTraffic(ctx, start, end, frame.size, frame.buckets)
				},
				oracle: func(ctx context.Context, tx *sql.Tx) (any, error) {
					return queryDashboardTrafficRaw(ctx, tx, start, end, frame.size, frame.buckets)
				},
			})
		}
	}
	return cases
}

// Every analytics reader returns through the rollups exactly what the raw
// events give, in every fold state (design §6.1): (a) fully folded, (b) the
// folder stopped at a random cursor, (c) journal rows pending after
// retention, repricing, anonymization and discard, (d) the journal applied.
func TestRollupReadersMatchRawEventsInEveryFoldState(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	random := rand.New(rand.NewPCG(51, 52))
	seedRollupEquivalence(t, s, random)
	cases := rollupEquivalenceCases(t, random)
	check := func(state string) int {
		t.Helper()
		return assertRollupReadsMatchRaw(t, s, state, cases)
	}
	foldRollups(t, s, 1000)
	check("(a) folded")
	var events int
	if err := s.db.QueryRow(`SELECT count(*) FROM usage_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	for round := range 2 {
		if err := s.RebuildUsageRollups(ctx); err != nil {
			t.Fatal(err)
		}
		// Stop at a random cursor through production-sized batches: one batch
		// of most of the events exceeds the writer's execution budget under -race.
		for target, folded := 1+random.IntN(events-1), 0; folded < target; {
			fold, err := s.FoldUsageRollups(ctx, min(1000, target-folded), 1000)
			if err != nil {
				t.Fatal(err)
			}
			if fold.Rows == 0 {
				break
			}
			folded += fold.Rows
		}
		check(fmt.Sprintf("(b) cursor %d", round))
		foldRollups(t, s, 1000)
	}
	declined := 0
	for round := range 3 {
		mutateRollupEquivalence(t, s, random, round)
		var journal int
		if err := s.db.QueryRow(`SELECT count(*) FROM usage_rollup_journal`).Scan(&journal); err != nil || journal == 0 {
			t.Fatalf("round %d left no pending journal: %d %v", round, journal, err)
		}
		declined += check(fmt.Sprintf("(c) journal %d after round %d", journal, round))
		foldRollups(t, s, 1000)
		if n := check(fmt.Sprintf("(d) applied round %d", round)); n != 0 {
			t.Fatalf("round %d: %d reads declined without pending journal", round, n)
		}
	}
	// Repricing journals cost-only rows, which every reader applies itself.
	if _, err := s.db.Exec(`UPDATE usage_events SET cost_microdollars=cost_microdollars+7 WHERE (rowid*?+?)%3=0`, rowSalt(random)...); err != nil {
		t.Fatal(err)
	}
	insertRollupFixtureAt(t, s, random, 20000, 50, rollupAutumnBase, 34*24*time.Hour)
	if n := check("(c) repricing journal and tail"); n != 0 {
		t.Fatalf("%d reads declined a cost-only journal", n)
	}
	t.Logf("%d cases per state, %d reads declined while removals were pending", len(cases), declined)
	assertRollupFixtureExercised(t, s)
}

// The hourly verifier compares the one-day dashboard and the seven-day
// report through both paths: a rollup row that differs outside the last day,
// which the table comparison does not recompute, is still a mismatch.
func TestUsageRollupVerifierComparesTheReadPaths(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	insertRollupFixtureAt(t, s, rand.New(rand.NewPCG(53, 54)), 0, 1500, rollupFixtureBase, 6*24*time.Hour)
	foldRollups(t, s, 1000)
	now := rollupFixtureBase.Add(5*24*time.Hour + 7*time.Hour + 123*time.Millisecond)
	if check, err := s.VerifyUsageRollups(ctx, now); err != nil || !check.Checked || check.Mismatch != "" {
		t.Fatalf("consistent rollups: %+v %v", check, err)
	}
	for _, test := range []struct{ corrupt, mismatch string }{
		{`UPDATE usage_rollup_total_hour SET cost_microdollars=cost_microdollars+1000000 WHERE bucket_epoch=?`, "report_7d"},
		{`UPDATE usage_rollup_conversation_hour SET conversation_id='conv-ghost' WHERE id=(SELECT id FROM usage_rollup_conversation_hour
 WHERE bucket_epoch>=? AND in_reports=1 ORDER BY bucket_epoch LIMIT 1)`, "report_7d"},
	} {
		hour := rollupFixtureBase.Add(2 * 24 * time.Hour).Unix()
		if _, err := s.db.Exec(test.corrupt, hour); err != nil {
			t.Fatal(err)
		}
		if check, err := s.VerifyUsageRollups(ctx, now); err != nil || check.Mismatch != test.mismatch {
			t.Fatalf("%s: %+v %v", test.corrupt, check, err)
		}
		rebuildRollups(t, s)
	}
	// The dashboard check covers the minutes of the first partial hour.
	if _, err := s.db.Exec(`UPDATE usage_rollup_total_hour SET requests=requests+1 WHERE bucket_epoch=?`,
		now.Add(-23*time.Hour).Truncate(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	tx, err := s.analyticsDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	state, err := loadRollupState(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	if mismatch, err := verifyDashboardRead(ctx, tx, state, now); err != nil || mismatch != "dashboard_1d" {
		t.Fatalf("dashboard check: %q %v", mismatch, err)
	}
}

// assertRollupFixtureExercised guards against a fixture whose reads are
// trivially equal because they are empty.
func assertRollupFixtureExercised(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	end := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	report, err := s.QueryReports(ctx, domain.ReportFilter{Start: end.AddDate(0, 0, -7), End: end, PreviousStart: end.AddDate(0, 0, -14)})
	if err != nil || report.Summary.TotalRequests < 300 || report.Summary.TotalConversations < 3 || report.Summary.ActiveAccounts < 3 ||
		report.Daily[3].MedianTTFTMS == 0 || len(report.ByModel) < 3 || len(report.ByUserAgent) < 4 || !report.Comparison.CanCompare {
		t.Fatalf("report fixture %+v %v", report.Summary, err)
	}
	traffic, err := s.QueryDashboardTraffic(ctx, end.Add(-24*time.Hour), end, 3600, 24)
	if err != nil || traffic.CurrentConversations == 0 || traffic.TopError == nil || traffic.Buckets[5].Requests == 0 {
		t.Fatalf("dashboard fixture %+v %v", traffic, err)
	}
	usage, err := s.KeyUsage7Day(ctx, "key-0", end.Add(-7*24*time.Hour), end)
	if err != nil || usage.TotalRequests == 0 || len(usage.AccountCosts) < 2 {
		t.Fatalf("key usage fixture %+v %v", usage, err)
	}
	options, err := s.RequestLogOptions(ctx, domain.RequestLogFilter{})
	if err != nil || len(options.AccountIDs) < 3 || len(options.APIKeys) != 2 || len(options.ModelOptions) < 6 || len(options.Statuses) < 5 {
		t.Fatalf("options fixture %+v %v", options, err)
	}
	conversations, err := s.ListConversations(ctx, domain.ConversationFilter{Limit: 10})
	if err != nil || conversations.Total < 3 || conversations.Conversations[0].RequestCount < 10 {
		t.Fatalf("conversations fixture %+v %v", conversations, err)
	}
	if details, err := s.ConversationDetails(ctx, "conv-a"); err != nil || len(details.ModelStats) < 3 || details.DominantUserAgentGroup == nil {
		t.Fatalf("conversation fixture %+v %v", details, err)
	}
}

// mutateRollupEquivalence changes folded events as the writers of every
// version do: retention, repricing, account history anonymization and
// discard, plus moves and deletions of single events, and inserts late events
// into folded hours.
func mutateRollupEquivalence(t *testing.T, s *Store, random *rand.Rand, round int) {
	t.Helper()
	ctx := context.Background()
	cutoff := rollupSpringBase.Add(time.Duration(2+round) * 24 * time.Hour)
	if _, err := s.FoldAndPruneRequestLogs(ctx, cutoff, 100+random.IntN(200)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE usage_events SET cost_microdollars=cost_microdollars+?
 WHERE (rowid*?+?)%9=0 AND model=?`, append([]any{1 + random.Int64N(5000)}, append(rowSalt(random), fmt.Sprintf("model-%d", random.IntN(3)))...)...); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE usage_events SET conversation_id=?,status=?,useragent_group=NULL
 WHERE (rowid*?+?)%40=0`, append([]any{[]any{"conv-b", " conv-c", nil}[round], []string{"success", "error", "cancelled"}[round]}, rowSalt(random)...)...); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM usage_events WHERE (rowid*?+?)%50=0`, rowSalt(random)...); err != nil {
		t.Fatal(err)
	}
	switch round {
	case 0:
		if err := s.DeleteAccount(ctx, "acct-1", false); err != nil {
			t.Fatal(err)
		}
	case 1:
		if err := s.DeleteAccount(ctx, "acct-0", true); err != nil {
			t.Fatal(err)
		}
	}
	insertRollupFixtureAt(t, s, random, 10000+round*100, 60, rollupSpringBase, 22*24*time.Hour)
}

// rowSalt selects a pseudo-random subset of rows as (rowid*a+b)%n=0 from the
// test's seeded generator: SQLite's random() made runs unrepeatable.
func rowSalt(random *rand.Rand) []any {
	return []any{1 + 2*random.Int64N(1<<20), random.Int64N(1 << 20)}
}
