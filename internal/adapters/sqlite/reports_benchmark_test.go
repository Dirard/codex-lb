package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

// BenchmarkUsageRollupReads compares every analytics read on the folded
// synthetic production-size database (742 k events over 30 days, design
// §4.2): "rollup" is the store method, which reads the rollups, "raw" the raw
// reader in its own read snapshot. Run explicitly:
// go test ./internal/adapters/sqlite -run '^$' -bench BenchmarkUsageRollupReads -benchtime=3x
func BenchmarkUsageRollupReads(b *testing.B) {
	ctx := context.Background()
	s, err := Open(filepath.Join(b.TempDir(), "reads.sqlite3"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = s.Close() })
	for n := range 9 {
		id := fmt.Sprintf("acct-%d", n)
		if err := s.SaveAccount(ctx, domain.Account{ID: id, Kind: domain.AccountChatGPT, Email: id + "@example.invalid",
			PlanType: "plus", Status: domain.AccountActive, CreatedAt: fixedTime}); err != nil {
			b.Fatal(err)
		}
	}
	for n := range 28 {
		if err := s.SaveAPIKey(ctx, testKey(fmt.Sprintf("key-%d", n), nil), fixedTime); err != nil {
			b.Fatal(err)
		}
	}
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	events := syntheticUsageEvents()
	if err := insertSyntheticUsageEvents(ctx, s.db, 0, events, start); err != nil {
		b.Fatal(err)
	}
	foldRollups(b, s, 5000)
	if _, err := s.CheckpointWAL(ctx); err != nil {
		b.Fatal(err)
	}
	var latest int64
	var busy string
	if err := s.db.QueryRow(`SELECT (SELECT max(requested_at) FROM usage_events),
 (SELECT api_key_id FROM usage_events GROUP BY api_key_id ORDER BY count(*) DESC LIMIT 1)`).Scan(&latest, &busy); err != nil {
		b.Fatal(err)
	}
	now := fromMillis(latest).Add(time.Minute)
	prague, err := time.LoadLocation("Europe/Prague")
	if err != nil {
		b.Fatal(err)
	}
	kolkata, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		b.Fatal(err)
	}
	reportIn := func(loc *time.Location, days int, keys ...string) domain.ReportFilter {
		end := time.Date(now.In(loc).Year(), now.In(loc).Month(), now.In(loc).Day(), 0, 0, 0, 0, loc)
		first := end.AddDate(0, 0, 1-days)
		return domain.ReportFilter{Start: first.UTC(), End: end.AddDate(0, 0, 1).UTC(),
			PreviousStart: first.AddDate(0, 0, -days).UTC(), Location: loc, APIKeyIDs: keys}
	}
	report := func(days int, keys ...string) domain.ReportFilter { return reportIn(prague, days, keys...) }
	week := now.Add(-7 * 24 * time.Hour)
	type read struct {
		name   string
		rollup func() error
		raw    func(tx *sql.Tx) error
	}
	var reads []read
	for _, frame := range []struct {
		name          string
		window        time.Duration
		size, buckets int
	}{{"overview_1d", 24 * time.Hour, 3600, 24}, {"overview_7d", 7 * 24 * time.Hour, 21600, 28}, {"overview_30d", 30 * 24 * time.Hour, 86400, 30}} {
		from := now.Add(-frame.window)
		reads = append(reads, read{frame.name, func() error {
			_, err := s.QueryDashboardTraffic(ctx, from, now, frame.size, frame.buckets)
			return err
		}, func(tx *sql.Tx) error {
			_, err := queryDashboardTrafficRaw(ctx, tx, from, now, frame.size, frame.buckets)
			return err
		}})
	}
	for _, test := range []struct {
		name   string
		filter domain.ReportFilter
	}{{"reports_7d", report(7)}, {"reports_30d", report(30)}, {"reports_90d", report(90)}, {"key_report_7d", report(7, busy)},
		{"reports_30d_kolkata", reportIn(kolkata, 30)}} {
		reads = append(reads, read{test.name, func() error {
			_, err := s.QueryReports(ctx, test.filter)
			return err
		}, func(tx *sql.Tx) error {
			_, err := queryReportsRaw(ctx, tx, test.filter)
			return err
		}})
	}
	reads = append(reads, read{"key_usage_7d", func() error {
		_, err := s.KeyUsage7Day(ctx, busy, week, now)
		return err
	}, func(tx *sql.Tx) error {
		_, err := keyUsage7DayRaw(ctx, tx, busy, week, now)
		return err
	}}, read{"key_trends_7d", func() error {
		_, err := s.KeyTrendBuckets(ctx, busy, week, now)
		return err
	}, func(tx *sql.Tx) error {
		_, err := keyTrendBucketsRaw(ctx, tx, busy, week, now)
		return err
	}})
	for _, test := range []struct {
		name   string
		filter domain.RequestLogFilter
	}{{"options_default", domain.RequestLogFilter{}}, {"options_7d", domain.RequestLogFilter{Since: &week}},
		{"options_7d_key", domain.RequestLogFilter{Since: &week, APIKeyIDs: []string{busy}}}} {
		reads = append(reads, read{test.name, func() error {
			_, err := s.RequestLogOptions(ctx, test.filter)
			return err
		}, func(tx *sql.Tx) error {
			_, err := requestLogOptionsRaw(ctx, tx, test.filter)
			return err
		}})
	}
	for _, test := range []struct {
		name   string
		filter domain.RequestLogFilter
	}{{"logs_total_default", domain.RequestLogFilter{Limit: 25}}, {"logs_total_7d_error", domain.RequestLogFilter{Limit: 25, Since: &week, Statuses: []string{"error"}}}} {
		reads = append(reads, read{test.name, func() error {
			// The total alone; the page stays a raw keyset read.
			tx, err := s.analyticsDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
			if err != nil {
				return err
			}
			defer tx.Rollback()
			reader, err := openRollupRead(ctx, tx)
			if err != nil || reader == nil {
				return fmt.Errorf("rollups unusable: %w", err)
			}
			_, err = reader.requestLogTotal(ctx, newPendingLogReader(tx), test.filter)
			return err
		}, func(tx *sql.Tx) error {
			_, err := requestLogTotalRaw(ctx, tx, newPendingLogReader(tx), test.filter)
			return err
		}})
	}
	for _, days := range []int{7, 30} {
		filter := domain.ConversationFilter{Since: now.Add(-time.Duration(days) * 24 * time.Hour), Limit: 50}
		reads = append(reads, read{fmt.Sprintf("conversations_%dd", days), func() error {
			_, err := s.ListConversations(ctx, filter)
			return err
		}, func(tx *sql.Tx) error {
			_, err := listConversationsRaw(ctx, tx, filter)
			return err
		}})
	}
	reads = append(reads, read{"conversation_details", func() error {
		_, err := s.ConversationDetails(ctx, "conv-1000")
		return err
	}, func(tx *sql.Tx) error {
		_, err := conversationDetailsRaw(ctx, tx, "conv-1000")
		return err
	}})
	for _, test := range reads {
		b.Run(test.name+"/rollup", func(b *testing.B) {
			for range b.N {
				if err := test.rollup(); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(test.name+"/raw", func(b *testing.B) {
			for range b.N {
				tx, err := s.analyticsDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
				if err != nil {
					b.Fatal(err)
				}
				err = test.raw(tx)
				_ = tx.Rollback()
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

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
