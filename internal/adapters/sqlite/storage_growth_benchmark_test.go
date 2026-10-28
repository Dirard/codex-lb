package sqlite

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

// Synthetic installation used for storage-growth before/after numbers:
// 300k request events over 30 days and one day of per-minute quota polling.
// Run explicitly:
//
//	go test ./internal/adapters/sqlite -run '^$' -bench BenchmarkStorageGrowth -benchtime=3x
const (
	growthBenchAccounts = 500
	growthBenchKeys     = 50
	growthBenchEvents   = 300000
)

func openGrowthBenchStore(b *testing.B) (*Store, time.Time) {
	b.Helper()
	ctx := context.Background()
	s, err := Open(filepath.Join(b.TempDir(), "growth.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = s.Close() })
	now := fixedTime.Truncate(time.Hour)
	at := millis(now)
	exec := func(query string, args ...any) {
		b.Helper()
		if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
			b.Fatalf("%v\n%s", err, query)
		}
	}
	exec(`WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i+1 FROM n WHERE i<?)
INSERT INTO accounts(id,kind,provider,email,plan_type,status,created_at)
SELECT printf('acct-%03d',i),'chatgpt','openai',printf('a%03d@example.invalid',i),'pro','active',? FROM n`, growthBenchAccounts-1, at)
	exec(`INSERT INTO account_quotas(account_id,window,used_percent,reset_at,window_minutes,observed_at)
SELECT id,'primary',10,?,300,? FROM accounts UNION ALL SELECT id,'secondary',20,?,10080,? FROM accounts`,
		at+3600000, at, at+86400000, at)
	for i := range growthBenchKeys {
		if err := s.SaveAPIKey(ctx, testKey(fmt.Sprintf("key-%d", i), nil), now); err != nil {
			b.Fatal(err)
		}
	}
	span := int64(30 * 24 * 3600 * 1000)
	exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<?)
INSERT INTO usage_reservations(id,api_key_id,account_id,model,status,created_at,updated_at,input_tokens,output_tokens)
SELECT printf('res-%09d',i),'key-'||(i%50),printf('acct-%03d',i%500),'model-'||(i%5),'finalized',
 ?-?+(i*?/?),?-?+(i*?/?),100000,500 FROM n`, growthBenchEvents, at, span, span, growthBenchEvents, at, span, span, growthBenchEvents)
	exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<?)
INSERT INTO usage_events(request_id,reservation_id,api_key_id,account_id,model,status,requested_at,
 conversation_id,reasoning_effort,transport,useragent_group,input_tokens,output_tokens,cached_input_tokens,cost_microdollars,
 queue_latency_ms,total_latency_ms,latency_first_token_ms)
SELECT printf('request-%09d',i),printf('res-%09d',i),'key-'||(i%50),printf('acct-%03d',i%500),'model-'||(i%5),
 CASE WHEN i%100=0 THEN 'error' ELSE 'success' END,?-?+(i*?/?),
 'conversation-'||(i%2500),'high','websocket','codex',100000,500,90000,50000,(i%50),2000+(i%3000),300+(i%700) FROM n`,
		growthBenchEvents, at, span, span, growthBenchEvents)
	// One day of per-minute polling of both windows for every account, as
	// written before history deduplication: 1440 rows per series.
	exec(`WITH RECURSIVE m(j) AS (SELECT 0 UNION ALL SELECT j+1 FROM m WHERE j<1439)
INSERT INTO account_quota_history(account_id,window,observed_at,used_percent,reset_at,window_minutes)
SELECT a.id,w.w,?-j*60000,(1439-j)/30*1.0,?+3600000,CASE w.w WHEN 'primary' THEN 300 ELSE 10080 END
 FROM accounts a,(SELECT 'primary' AS w UNION ALL SELECT 'secondary') w,m`, at, at)
	return s, now
}

func BenchmarkStorageGrowth(b *testing.B) {
	s, now := openGrowthBenchStore(b)
	ctx := context.Background()
	b.Run("RecentAccountQuotaHistory_500_accounts", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			if _, err := s.RecentAccountQuotaHistory(ctx, now.Add(-30*24*time.Hour), now); err != nil {
				b.Fatal(err)
			}
		}
	})
	service := application.NewReportsService(s, func() time.Time { return now })
	b.Run("DashboardOverview_7d_500_accounts", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			if _, err := service.DashboardOverview(ctx, "7d", s); err != nil {
				b.Fatal(err)
			}
		}
	})
	for _, days := range []int{7, 30} {
		b.Run(fmt.Sprintf("QueryReports_%dd", days), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				filter := domain.ReportFilter{Start: now.AddDate(0, 0, -days), End: now,
					PreviousStart: now.AddDate(0, 0, -2*days), Location: time.UTC}
				if _, err := s.QueryReports(ctx, filter); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
	b.Run("ListConversations", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			if _, err := s.ListConversations(ctx, domain.ConversationFilter{Limit: 25}); err != nil {
				b.Fatal(err)
			}
		}
	})
	for _, offset := range []int{0, 250000} {
		b.Run(fmt.Sprintf("ListRequestLogs_offset_%d", offset), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				if _, err := s.ListRequestLogs(ctx, domain.RequestLogFilter{Limit: 25, Offset: offset}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
	b.Run("ListRequestLogs_cursor_after_250000", func(b *testing.B) {
		deep, err := s.ListRequestLogs(ctx, domain.RequestLogFilter{Limit: 25, Offset: 249975})
		if err != nil || deep.NextCursor == nil {
			b.Fatalf("cursor setup: %v", err)
		}
		cursor, err := domain.ParseRequestLogCursor(*deep.NextCursor)
		if err != nil {
			b.Fatal(err)
		}
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			if _, err := s.ListRequestLogs(ctx, domain.RequestLogFilter{Limit: 25, Before: &cursor}); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("RequestLogOptions", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			if _, err := s.RequestLogOptions(ctx, domain.RequestLogFilter{}); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkStorageGrowthSteadyHistory measures the dashboard history reads on
// history in its steady shape: one row per five-minute bucket for the last day
// and one per hour for the six days before (bucket coalescing plus thinning).
func BenchmarkStorageGrowthSteadyHistory(b *testing.B) {
	s, now := openGrowthBenchStore(b)
	ctx := context.Background()
	at := millis(now)
	if _, err := s.db.ExecContext(ctx, `DELETE FROM account_quota_history`); err != nil {
		b.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `WITH RECURSIVE m(j) AS (SELECT 0 UNION ALL SELECT j+1 FROM m WHERE j<431)
INSERT INTO account_quota_history(account_id,window,observed_at,used_percent,reset_at,window_minutes)
SELECT a.id,w.w,CASE WHEN j<288 THEN ?-j*300000 ELSE ?-86400000-(j-288)*3600000 END,(431-j)/30*1.0,
 ?+86400000,CASE w.w WHEN 'primary' THEN 300 ELSE 10080 END
 FROM accounts a,(SELECT 'primary' AS w UNION ALL SELECT 'secondary') w,m`, at, at, at); err != nil {
		b.Fatal(err)
	}
	b.Run("RecentAccountQuotaHistory_500_accounts", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			if _, err := s.RecentAccountQuotaHistory(ctx, now.Add(-30*24*time.Hour), now); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("PositiveQuotaDeltas_500_accounts", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			if _, err := s.PositiveQuotaDeltas(ctx, now.Add(-7*24*time.Hour), now); err != nil {
				b.Fatal(err)
			}
		}
	})
	service := application.NewReportsService(s, func() time.Time { return now })
	b.Run("DashboardOverview_7d_500_accounts", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			if _, err := service.DashboardOverview(ctx, "7d", s); err != nil {
				b.Fatal(err)
			}
		}
	})
}
