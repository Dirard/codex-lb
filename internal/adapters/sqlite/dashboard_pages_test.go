package sqlite

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

// dashboardScale is the size of a synthetic installation for the page
// budget (dashboard redesign use case 10).
type dashboardScale struct {
	keys, groups, accounts int
	days, eventsPerKeyHour int
}

// dashboardNow is the evaluation instant of the scale fixtures: not on an
// hour, a few days after the Europe/Prague autumn daylight-saving change.
var dashboardNow = time.Date(2026, 10, 28, 10, 37, 12, 345000000, time.UTC)

// seedDashboardScale fills s with accounts (plus and pro, a few needing
// re-login, paused or limited), overlapping groups with limit templates, keys
// (a tenth ungrouped, some near or over a limit, some expiring) with active
// limits, current quotas, six hours of quota history and events: every key
// active in every hour of the days before now, with errors, cancellations and
// client disconnects.
func seedDashboardScale(t testing.TB, s *Store, scale dashboardScale) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("%v\n%s", err, query)
		}
	}
	now := millis(dashboardNow)
	for a := range scale.accounts {
		status := "active"
		switch a % 25 {
		case 3:
			status = "reauth_required"
		case 7:
			status = "paused"
		case 11:
			status = "rate_limited"
		}
		plan := []string{"plus", "pro"}[a%2]
		exec(`INSERT INTO accounts(id,kind,provider,email,alias,plan_type,status,created_at,chatgpt_account_id)
 VALUES(?,'chatgpt','openai',?,?,?,?,?,?)`, fmt.Sprintf("acct-%d", a), fmt.Sprintf("acct-%d@example.invalid", a),
			[]string{"", fmt.Sprintf("Account %d", a)}[a%2], plan, status, millis(fixedTime), fmt.Sprintf("chatgpt-%d", a))
		for w, window := range []string{"primary", "secondary"} {
			minutes := []int{300, 10080}[w]
			reset := now + int64((a%5+1)*[]int{40, 600}[w])*60000
			used := float64((a*37+w*11)%100) + 0.5
			exec(`INSERT INTO account_quotas(account_id,window,used_percent,reset_at,window_minutes,observed_at) VALUES(?,?,?,?,?,?)`,
				fmt.Sprintf("acct-%d", a), window, used, reset, minutes, now-30000)
			for p := range 72 {
				exec(`INSERT INTO account_quota_history(account_id,window,observed_at,used_percent,reset_at,window_minutes)
 VALUES(?,?,?,?,?,?)`, fmt.Sprintf("acct-%d", a), window, now-int64(72-p)*300000, max(0, used-float64(72-p)*0.1), reset, minutes)
			}
		}
	}
	for g := range scale.groups {
		id := fmt.Sprintf("grp-%d", g)
		exec(`INSERT INTO account_groups(id,name,created_at) VALUES(?,?,?)`, id, fmt.Sprintf("Group %02d", g), millis(fixedTime))
		for m := range 3 {
			exec(`INSERT OR IGNORE INTO account_group_accounts(group_id,account_id) VALUES(?,?)`, id,
				fmt.Sprintf("acct-%d", (g*2+m)%scale.accounts))
		}
		exec(`INSERT INTO group_limits(group_id,limit_type,limit_window,model_filter,max_value) VALUES(?,'cost_usd','weekly','',50000000)`, id)
	}
	for k := range scale.keys {
		var group any
		if k%10 != 0 {
			group = fmt.Sprintf("grp-%d", k%scale.groups)
		}
		var expires any
		if k%17 == 0 {
			expires = now + int64(k%9+1)*12*3600000
		}
		exec(`INSERT INTO api_keys(id,name,key_hash,key_prefix,group_id,is_active,expires_at,created_at,last_used_at)
 VALUES(?,?,?,?,?,?,?,?,?)`, fmt.Sprintf("key-%d", k), fmt.Sprintf("Key %03d", k), fmt.Sprintf("%064x", k),
			fmt.Sprintf("sk-clb-%03d", k), group, k%23 != 5, expires, millis(fixedTime), now-int64(k)*60000)
		exec(`INSERT INTO api_key_limits(api_key_id,limit_type,limit_window,model_filter,max_value,current_value,reset_at,is_active)
 VALUES(?,'cost_usd','weekly','',50000000,?,?,1)`, fmt.Sprintf("key-%d", k), int64(k%13)*4000000, now+3*24*3600000)
	}
	exec(`INSERT INTO api_keys(id,name,key_hash,key_prefix,is_active,created_at,deleted_at) VALUES('key-deleted','deleted',?,'sk-del',0,?,?)`,
		fmt.Sprintf("%064x", 1<<40), millis(fixedTime), now)
	hours := scale.days * 24
	first := floorRollupHour(now) - int64(hours)*rollupHourMS
	exec(`WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i+1 FROM n WHERE i<?)
INSERT INTO usage_events(request_id,api_key_id,account_id,model,status,error_code,request_kind,requested_at,
 conversation_id,useragent_group,input_tokens,output_tokens,cached_input_tokens,reasoning_tokens,reasoning_tokens_known,
 cost_microdollars,queue_latency_ms,total_latency_ms,latency_first_token_ms)
SELECT printf('scale-%08d',i),'key-'||(i%?),'acct-'||((i%?)%?),'model-'||(i%5),
 CASE i%41 WHEN 0 THEN 'error' WHEN 1 THEN 'cancelled' WHEN 2 THEN 'error' ELSE 'success' END,
 CASE i%41 WHEN 0 THEN 'upstream_error' WHEN 2 THEN 'client_disconnected' ELSE '' END,
 'normal',?+(i/(?*?))*3600000+((i*7919)%3600000),'conv-'||(i%997),'ua-'||(i%3),
 500+i%9000,50+i%900,i%400,i%90,1,1000+(i*31)%200000,i%50,900+i%40000,100+i%3000
FROM n WHERE ?+(i/(?*?))*3600000+((i*7919)%3600000)<?`,
		scale.keys*hours*scale.eventsPerKeyHour+scale.keys*scale.eventsPerKeyHour-1, scale.keys, scale.keys, scale.accounts,
		first, scale.keys, scale.eventsPerKeyHour, first, scale.keys, scale.eventsPerKeyHour, now)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	foldRollups(t, s, 5000)
}

// overviewAt runs the overview use case on a store at dashboardNow.
func overviewAt(t testing.TB, s *Store, loc *time.Location) domain.OverviewResponse {
	t.Helper()
	service := application.NewOverviewService(s, application.NewShellStatusService(s, nil), func() time.Time { return dashboardNow })
	response, err := service.Overview(context.Background(), loc)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

// The overview, the directory and the shell status execute the same number
// of statements for 30 keys, 5 groups and 10 accounts as for 300 keys, 50
// groups and 100 accounts (R2, R5).
func TestDashboardReadsUseAStatementCountIndependentOfScale(t *testing.T) {
	prague, err := time.LoadLocation("Europe/Prague")
	if err != nil {
		t.Fatal(err)
	}
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
		measure("overview", func() {
			response := overviewAt(t, s, prague)
			if len(response.Groups) != scale.groups+1 {
				t.Fatalf("%d group rows for %d groups", len(response.Groups), scale.groups)
			}
		})
		measure("directory", func() {
			if _, err := s.Directory(context.Background(), dashboardNow); err != nil {
				t.Fatal(err)
			}
		})
		measure("shell", func() {
			if _, err := application.NewShellStatusService(s, nil).Status(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
	for name, n := range counts {
		t.Logf("%s: %d statements at both scales", name, n[1])
		if n[0] != n[1] || n[0] == 0 {
			t.Errorf("%s: %d statements at small scale, %d at target scale", name, n[0], n[1])
		}
	}
}

// The overview answers within its 300 ms budget, warm, for 300 keys, 50
// groups and 100 accounts with every key active in every hour of the 8 days
// it reads (R49). The race detector slows SQLite about 40-fold, so it only
// checks the shape there.
func TestOverviewMeetsItsLatencyBudgetAtTargetScale(t *testing.T) {
	scale := dashboardScale{keys: 300, groups: 50, accounts: 100, days: 8, eventsPerKeyHour: 1}
	if raceDetector {
		scale.days = 1
	}
	s, _ := testStore(t)
	seedDashboardScale(t, s, scale)
	prague, err := time.LoadLocation("Europe/Prague")
	if err != nil {
		t.Fatal(err)
	}
	response := overviewAt(t, s, prague)
	if !response.Shell.RollupsUsable || len(response.AttentionKeys) != domain.AttentionKeysLimit || response.Pool.Trend7d == nil {
		t.Fatalf("unexpected overview: usable %v, %d attention keys, trend %v", response.Shell.RollupsUsable,
			len(response.AttentionKeys), response.Pool.Trend7d)
	}
	var runs []time.Duration
	for range 5 {
		start := time.Now()
		overviewAt(t, s, prague)
		runs = append(runs, time.Since(start))
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i] < runs[j] })
	t.Logf("overview at 300 keys / 50 groups / 100 accounts, %d events: median %v (min %v, max %v)",
		scale.keys*scale.days*24*scale.eventsPerKeyHour, runs[2], runs[0], runs[4])
	if !raceDetector && runs[2] > 300*time.Millisecond {
		t.Fatalf("overview median %v exceeds the 300 ms budget", runs[2])
	}
}

// BenchmarkDashboardPages measures the page reads at the target scale of R49:
// 300 keys, 50 groups, 100 accounts and about 1.5 million events over 30
// days. Run explicitly:
// go test ./internal/adapters/sqlite -run '^$' -bench BenchmarkDashboardPages -benchtime=10x
func BenchmarkDashboardPages(b *testing.B) {
	s, err := Open(b.TempDir() + "/pages.sqlite3")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = s.Close() })
	seedDashboardScale(b, s, dashboardScale{keys: 300, groups: 50, accounts: 100, days: 30, eventsPerKeyHour: 7})
	if _, err := s.CheckpointWAL(context.Background()); err != nil {
		b.Fatal(err)
	}
	for _, zone := range []string{"UTC", "Europe/Prague", "Asia/Kolkata"} {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			b.Fatal(err)
		}
		b.Run("overview_"+zone, func(b *testing.B) {
			for range b.N {
				overviewAt(b, s, loc)
			}
		})
	}
	b.Run("directory", func(b *testing.B) {
		for range b.N {
			if _, err := s.Directory(context.Background(), dashboardNow); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("shell", func(b *testing.B) {
		shell := application.NewShellStatusService(s, nil)
		for range b.N {
			if _, err := shell.Status(context.Background()); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("request_logs_24h", func(b *testing.B) {
		since := dashboardNow.Add(-24 * time.Hour)
		for range b.N {
			if _, err := s.ListRequestLogs(context.Background(), domain.RequestLogFilter{Limit: 25, Since: &since}); err != nil {
				b.Fatal(err)
			}
		}
	})
	benchmarkKeyPages(b, s)
	benchmarkKeyUsageReports(b, s)
	benchmarkKeysList(b, s)
}
