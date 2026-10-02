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

// Run explicitly: go test ./internal/adapters/sqlite -run '^$' -bench BenchmarkAdminReports -benchtime=3x
func BenchmarkAdminReports(b *testing.B) {
	ctx := context.Background()
	s, err := Open(filepath.Join(b.TempDir(), "reports.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = s.Close() })
	for n := range 8 {
		id := fmt.Sprintf("account-%d", n)
		if err := s.SaveAccount(ctx, domain.Account{ID: id, Kind: domain.AccountChatGPT,
			Email: id + "@example.invalid", PlanType: "plus", Status: domain.AccountActive, CreatedAt: fixedTime}); err != nil {
			b.Fatal(err)
		}
	}
	for n := range 20 {
		if err := s.SaveAPIKey(ctx, testKey(fmt.Sprintf("key-%d", n), nil), fixedTime); err != nil {
			b.Fatal(err)
		}
	}
	const size = 263609
	_, err = s.db.ExecContext(ctx, `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<?)
INSERT INTO usage_events(request_id,api_key_id,account_id,model,status,requested_at,
 conversation_id,reasoning_effort,transport,input_tokens,output_tokens,cached_input_tokens,cost_microdollars)
SELECT printf('request-%09d',i),'key-'||(i%20),'account-'||(i%8),'model-'||(i%5),
 CASE WHEN i%100=0 THEN 'error' ELSE 'success' END,?+(i*2592000000/?),
 'conversation-'||(i%2500),'high','websocket',100000,500,90000,50000 FROM n`, size, millis(fixedTime.AddDate(0, 0, -30)), size)
	if err != nil {
		b.Fatal(err)
	}
	for n := range 12 {
		_, err := s.db.ExecContext(ctx, `INSERT INTO usage_reservations(id,api_key_id,account_id,model,status,created_at,updated_at,needs_reconciliation)
VALUES(?, 'key-0','account-0','model-0','reserved',?,?,1)`, fmt.Sprintf("pending-%d", n), millis(fixedTime.Add(-time.Duration(n)*time.Hour)), millis(fixedTime))
		if err != nil {
			b.Fatal(err)
		}
	}
	for _, offset := range []int{0, 25, 2500, 250000} {
		b.Run(fmt.Sprintf("logs_offset_%d", offset), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				page, err := s.ListRequestLogs(ctx, domain.RequestLogFilter{Limit: 25, Offset: offset})
				if err != nil || page.Total != size+12 || len(page.Requests) != 25 {
					b.Fatalf("total=%d rows=%d error=%v", page.Total, len(page.Requests), err)
				}
			}
		})
	}
	b.Run("options", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			options, err := s.RequestLogOptions(ctx, domain.RequestLogFilter{})
			if err != nil || len(options.APIKeys) != 20 || len(options.AccountIDs) != 8 || len(options.Statuses) != 3 {
				b.Fatalf("options=%+v error=%v", options, err)
			}
		}
	})
	service := application.NewReportsService(s, func() time.Time { return fixedTime })
	b.Run("overview_7d", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			overview, err := service.DashboardOverview(ctx, "7d", s)
			if err != nil || len(overview.Accounts) != 8 || len(overview.Trends.Requests) != 28 {
				b.Fatalf("overview accounts=%d buckets=%d error=%v", len(overview.Accounts), len(overview.Trends.Requests), err)
			}
		}
	})
}
