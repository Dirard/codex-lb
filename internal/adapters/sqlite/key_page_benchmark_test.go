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

// busyKeyID is the busiest key of the key page fixtures.
const busyKeyID = "key-busy"

// seedBusyKey adds a grouped key with a cost limit and a model-filtered
// token limit and perDay events over the days before dashboardNow on 3
// accounts, 4 models and 2 user-agent groups (24 cube rows per hour, as the
// busiest production key), with errors and cancellations, then folds.
func seedBusyKey(t testing.TB, s *Store, days, perDay int) {
	t.Helper()
	now := millis(dashboardNow)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := s.db.Exec(query, args...); err != nil {
			t.Fatalf("%v\n%s", err, query)
		}
	}
	exec(`INSERT INTO api_keys(id,name,key_hash,key_prefix,group_id,is_active,created_at,last_used_at)
 VALUES(?,'Busy key',?,'sk-clb-busy','grp-1',1,?,?)`, busyKeyID, fmt.Sprintf("%064x", 1<<41), millis(fixedTime), now)
	exec(`INSERT INTO api_key_limits(api_key_id,limit_type,limit_window,model_filter,max_value,current_value,reset_at,is_active)
 VALUES(?,'cost_usd','weekly','',500000000,300000000,?,1),(?,'total_tokens','daily','model-1',90000000,20000000,?,1)`,
		busyKeyID, now+3*24*3600000, busyKeyID, now+5*3600000)
	events := days * perDay
	step := int64(days) * 86400000 / int64(events)
	exec(`WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i+1 FROM n WHERE i<?)
INSERT INTO usage_events(request_id,api_key_id,account_id,model,status,error_code,request_kind,requested_at,
 conversation_id,useragent_group,input_tokens,output_tokens,cached_input_tokens,reasoning_tokens,reasoning_tokens_known,
 cost_microdollars,queue_latency_ms,total_latency_ms,latency_first_token_ms,client_ip,transport)
SELECT printf('busy-%08d',i),?,'acct-'||(i%3),'model-'||(i%4),
 CASE i%53 WHEN 0 THEN 'error' WHEN 1 THEN 'cancelled' WHEN 2 THEN 'quota' ELSE 'success' END,
 CASE i%53 WHEN 0 THEN 'upstream_error' WHEN 2 THEN 'rate_limited' ELSE '' END,
 'normal',?-i*?,'conv-busy-'||(i%311),'ua-'||(i%2),
 800+i%7000,60+i%700,i%500,i%80,1,2000+(i*37)%150000,i%40,1200+i%30000,150+i%2500,'198.51.100.'||(i%200),'websocket'
FROM n`, events-1, busyKeyID, now-1000, step)
	foldRollups(t, s, 5000)
}

// keyPageAt runs the administrator key page use case at dashboardNow.
func keyPageAt(t testing.TB, s *Store, keyID string, days int, loc *time.Location) domain.KeyPageResponse {
	t.Helper()
	service := application.NewKeyPageService(s, application.NewShellStatusService(s, nil), func() time.Time { return dashboardNow })
	local := dashboardNow.In(loc)
	page, err := service.AdminPage(context.Background(), keyID, application.KeyPageParams{Location: loc,
		StartDate: local.AddDate(0, 0, 1-days).Format("2006-01-02"), EndDate: local.Format("2006-01-02")})
	if err != nil {
		t.Fatal(err)
	}
	return page
}

// The key page of the busiest key answers within the 300 ms budget, warm,
// for 30 days next to 300 keys, 50 groups and 100 accounts (R49). The race
// detector slows SQLite about 40-fold, so it only checks the shape there.
func TestKeyPageMeetsItsLatencyBudgetAtTargetScale(t *testing.T) {
	scale, days, perDay := dashboardScale{keys: 300, groups: 50, accounts: 100, days: 1, eventsPerKeyHour: 1}, 30, 2000
	if raceDetector {
		scale, days, perDay = dashboardScale{keys: 30, groups: 5, accounts: 10, days: 1, eventsPerKeyHour: 1}, 7, 200
	}
	s, _ := testStore(t)
	seedDashboardScale(t, s, scale)
	seedBusyKey(t, s, days, perDay)
	prague, err := time.LoadLocation("Europe/Prague")
	if err != nil {
		t.Fatal(err)
	}
	page := keyPageAt(t, s, busyKeyID, days, prague)
	if !page.RollupsUsable || page.Summary.Requests < int64(days*perDay)*9/10 || len(page.ByModel) != 4 || len(page.UserAgents) != 2 ||
		len(page.ErrorsByCode) != 2 || len(page.RecentRequests) != domain.KeyPageRecentLimit || page.Limits[0].PacePerHour == nil ||
		page.Group == nil || page.Pool == nil || page.Summary.TotalP95MS == nil {
		t.Fatalf("unexpected key page: %+v", page.Summary)
	}
	var runs []time.Duration
	for range 5 {
		start := time.Now()
		keyPageAt(t, s, busyKeyID, days, prague)
		runs = append(runs, time.Since(start))
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i] < runs[j] })
	t.Logf("key page of the busiest key (%d events over %d days): median %v (min %v, max %v)", days*perDay, days, runs[2], runs[0], runs[4])
	if !raceDetector && runs[2] > 300*time.Millisecond {
		t.Fatalf("key page median %v exceeds the 300 ms budget", runs[2])
	}
}

// benchmarkKeyPages measures the key page of the busiest key (57.7 k events
// in 7 days, as in production) for 7 and 30 days, a typical key for 30
// days, and the recent-request pages, on the target-scale store of
// BenchmarkDashboardPages.
func benchmarkKeyPages(b *testing.B, s *Store) {
	seedBusyKey(b, s, 30, 8243)
	for _, zone := range []string{"UTC", "Europe/Prague", "Asia/Kolkata"} {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			b.Fatal(err)
		}
		for _, days := range []int{7, 30} {
			b.Run(fmt.Sprintf("key_page_busy_%dd_%s", days, zone), func(b *testing.B) {
				for range b.N {
					keyPageAt(b, s, busyKeyID, days, loc)
				}
			})
		}
	}
	b.Run("key_page_typical_30d_UTC", func(b *testing.B) {
		for range b.N {
			keyPageAt(b, s, "key-7", 30, time.UTC)
		}
	})
	for _, errorsOnly := range []bool{false, true} {
		b.Run(fmt.Sprintf("key_requests_busy_errors_%v", errorsOnly), func(b *testing.B) {
			for range b.N {
				if _, err := s.KeyRecentRequests(context.Background(), domain.KeyRequestsQuery{KeyID: busyKeyID, Limit: 50, ErrorsOnly: errorsOnly}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
