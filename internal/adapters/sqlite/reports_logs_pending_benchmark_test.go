package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

// pendingLogShare is the share of reservations, in units of 1/10000, left
// reserved and flagged for reconciliation: production holds 73.8 k of 1.6 M.
const pendingLogShare = 462

// seedPendingLogAccounts saves the accounts, keys and deletions the pending
// fixture refers to: acct-7 is deleted, acct-8 was deleted in generation 0
// and reimported as generation 1, and acct-0 and acct-3 carry aliases.
func seedPendingLogAccounts(ctx context.Context, s *Store, keys int) error {
	for n := range 9 {
		id := fmt.Sprintf("acct-%d", n)
		alias := map[int]string{0: "alpha", 3: "gamma"}[n]
		if err := s.SaveAccount(ctx, domain.Account{ID: id, Kind: domain.AccountChatGPT, Email: id + "@example.invalid",
			Alias: alias, PlanType: "plus", Status: domain.AccountActive, CreatedAt: fixedTime}); err != nil {
			return err
		}
	}
	for n := range keys {
		key := testKey(fmt.Sprintf("key-%d", n), nil)
		if n%2 == 0 {
			key.Name = fmt.Sprintf("team-%d", n%5)
		}
		if err := s.SaveAPIKey(ctx, key, fixedTime); err != nil {
			return err
		}
	}
	if err := s.ensureInternalKey(ctx, domain.WarmupKeyID, "Admin warmup"); err != nil {
		return err
	}
	for _, statement := range []string{
		`UPDATE accounts SET generation=1 WHERE id='acct-8'`,
		`INSERT INTO account_deletions(account_id,generation,delete_history,cleanup_done) VALUES('acct-7',0,0,1),('acct-8',0,0,1)`,
	} {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

// insertPendingLogReservations adds n reservations created evenly over span
// from start in insertion order, with ids in hash order. About 4.6 % stay
// reserved and flagged for reconciliation, mostly without an account like
// imported legacy reservations, some on the warmup key or on deleted account
// generations; 0.02 % are live; the rest are finalized (odd rows) or released.
func insertPendingLogReservations(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, n, keys int, start time.Time, span time.Duration) error {
	_, err := db.ExecContext(ctx, `WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i+1 FROM n WHERE i<?),
 h AS (SELECT i,(i*2654435761)%4294967296 AS h FROM n),
 g AS (SELECT i,h,h%10000<? AS pending,h%10000 BETWEEN ? AND ?+1 AS live FROM h)
INSERT INTO usage_reservations(id,api_key_id,account_id,account_generation,model,status,created_at,updated_at,
 needs_reconciliation,owner_instance)
SELECT printf('%08x-%07d',h,i),
 CASE WHEN pending AND h%37=0 THEN '`+domain.WarmupKeyID+`' ELSE 'key-'||(h/7%?) END,
 CASE WHEN pending AND h%10<7 THEN '' ELSE 'acct-'||(h/3%9) END,
 CASE WHEN h/3%9=8 AND h%2=0 THEN 1 ELSE 0 END,
 CASE WHEN pending AND h%11=0 THEN 'legacy-model' ELSE 'model-'||(h/5%5) END,
 CASE WHEN pending OR live THEN 'reserved' WHEN i%2=1 THEN 'finalized' ELSE 'released' END,
 ?+i*?/?, ?+i*?/?, pending, CASE WHEN live THEN 'instance' ELSE '' END FROM g`,
		n-1, pendingLogShare, pendingLogShare, pendingLogShare, keys,
		millis(start), span.Milliseconds(), n, millis(start), span.Milliseconds(), n)
	return err
}

// linkPendingLogEvents points every event whose reservation (by rowid 2 x
// event rowid) was finalized at it, and records an event for one in 250
// pending reservations, which must leave the request log's pending entries.
func linkPendingLogEvents(ctx context.Context, s *Store) error {
	for _, statement := range []string{
		`UPDATE usage_events SET reservation_id=(SELECT r.id FROM usage_reservations r
 WHERE r.rowid=2*usage_events.rowid AND r.status='finalized')`,
		`INSERT INTO usage_events(request_id,reservation_id,api_key_id,account_id,model,status,requested_at,input_tokens,
 output_tokens,cost_microdollars)
 SELECT 'recorded-'||r.id,r.id,nullif(r.api_key_id,'` + domain.WarmupKeyID + `'),nullif(r.account_id,''),r.model,'success',
 r.created_at,10,5,700 FROM usage_reservations r WHERE r.status='reserved' AND r.needs_reconciliation=1 AND r.rowid%250=0`,
	} {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

// BenchmarkRequestLogPending compares the request-log reads before (the
// reference: pending entries through the pending_log_entries CTE) and after
// (the pending index) on a production-shaped database: 742 k folded events
// and 1.6 M reservations, 74 k of them pending and 295 of those already with
// an event. Reservation ids are in hash order, so the probes of the event
// index interleave with its keys (the worst case). The pending_* reads are
// the pending contribution alone. warm repeats a read; cold first drops the
// database from the OS page cache. index_build creates the pending index of
// migration 43 on the 1.6 M reservations. Run explicitly:
// go test ./internal/adapters/sqlite -run '^$' -bench BenchmarkRequestLogPending -benchtime=5x
func BenchmarkRequestLogPending(b *testing.B) {
	ctx := context.Background()
	path := filepath.Join(b.TempDir(), "pending.sqlite3")
	s, err := Open(path)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = s.Close() })
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	reservations, events := 1600000, syntheticUsageEvents()
	if raceDetector {
		reservations = 40000
	}
	if err := seedPendingLogAccounts(ctx, s, 28); err != nil {
		b.Fatal(err)
	}
	if err := insertPendingLogReservations(ctx, s.db, reservations, 28, start, 30*24*time.Hour); err != nil {
		b.Fatal(err)
	}
	if err := insertSyntheticUsageEvents(ctx, s.db, 0, events, start); err != nil {
		b.Fatal(err)
	}
	if err := linkPendingLogEvents(ctx, s); err != nil {
		b.Fatal(err)
	}
	foldRollups(b, s, 5000)
	if _, err := s.CheckpointWAL(ctx); err != nil {
		b.Fatal(err)
	}
	var latest int64
	var busy string
	var pending, recorded int
	if err := s.db.QueryRow(`SELECT (SELECT max(requested_at) FROM usage_events),
 (SELECT api_key_id FROM usage_events GROUP BY api_key_id ORDER BY count(*) DESC LIMIT 1),
 (SELECT count(*) FROM usage_reservations WHERE status='reserved' AND needs_reconciliation=1),
 (SELECT count(*) FROM usage_events WHERE request_id LIKE 'recorded-%')`).Scan(&latest, &busy, &pending, &recorded); err != nil {
		b.Fatal(err)
	}
	b.Logf("%d reservations, %d flagged for reconciliation, %d of them recorded, %d events", reservations, pending, recorded, events)
	week := fromMillis(latest).Add(-7 * 24 * time.Hour)
	middle, err := s.ListRequestLogs(ctx, domain.RequestLogFilter{Limit: 1, Offset: 2000})
	if err != nil || middle.NextCursor == nil {
		b.Fatal(middle, err)
	}
	cursor, err := domain.ParseRequestLogCursor(*middle.NextCursor)
	if err != nil {
		b.Fatal(err)
	}
	inTx := func(read func(tx *sql.Tx) error) error {
		tx, err := s.analyticsDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return err
		}
		defer tx.Rollback()
		return read(tx)
	}
	drain := func(rows *sql.Rows, err error) error {
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
		}
		return rows.Err()
	}
	type read struct {
		name          string
		before, after func() error
	}
	var reads []read
	for _, test := range []struct {
		name   string
		filter domain.RequestLogFilter
	}{{"options_default", domain.RequestLogFilter{}}, {"options_7d", domain.RequestLogFilter{Since: &week}},
		{"options_7d_key", domain.RequestLogFilter{Since: &week, APIKeyIDs: []string{busy}}},
		{"options_search", domain.RequestLogFilter{Search: "team-1"}}} {
		reads = append(reads, read{test.name, func() error {
			_, err := s.legacyRequestLogOptions(ctx, test.filter)
			return err
		}, func() error {
			_, err := s.RequestLogOptions(ctx, test.filter)
			return err
		}})
	}
	for _, test := range []struct {
		name   string
		filter domain.RequestLogFilter
	}{{"logs_default", domain.RequestLogFilter{Limit: 25}}, {"logs_7d", domain.RequestLogFilter{Limit: 25, Since: &week}},
		{"logs_key", domain.RequestLogFilter{Limit: 25, APIKeyIDs: []string{busy}}},
		{"logs_keyset", domain.RequestLogFilter{Limit: 25, Before: &cursor}},
		{"logs_offset_5000", domain.RequestLogFilter{Limit: 25, Offset: 5000}},
		{"logs_search", domain.RequestLogFilter{Limit: 25, Search: "team-1"}}} {
		reads = append(reads, read{test.name, func() error {
			_, err := s.legacyListRequestLogs(ctx, test.filter)
			return err
		}, func() error {
			_, err := s.ListRequestLogs(ctx, test.filter)
			return err
		}})
	}
	reads = append(reads, read{"pending_facets", func() error {
		return inTx(func(tx *sql.Tx) error {
			return drain(tx.QueryContext(ctx, legacyPendingRequestLogSource+`SELECT e.account_id,e.api_key_id,e.model,
 e.reasoning_effort,e.status FROM pending_log_entries e WHERE 1=1`))
		})
	}, func() error {
		return inTx(func(tx *sql.Tx) error {
			_, err := pendingLogGroups(ctx, tx, domain.RequestLogFilter{})
			return err
		})
	}}, read{"pending_total", func() error {
		return inTx(func(tx *sql.Tx) error {
			return drain(tx.QueryContext(ctx, legacyPendingRequestLogSource+`SELECT count(*) FROM pending_log_entries e WHERE 1=1`))
		})
	}, func() error {
		return inTx(func(tx *sql.Tx) error {
			_, err := newPendingLogReader(tx).count(ctx, domain.RequestLogFilter{})
			return err
		})
	}}, read{"pending_page", func() error {
		return inTx(func(tx *sql.Tx) error {
			return drain(tx.QueryContext(ctx, legacyPendingRequestLogSource+`SELECT e.request_id FROM pending_log_entries e
 ORDER BY e.requested_at DESC,e.request_id DESC LIMIT 26`))
		})
	}, func() error {
		return inTx(func(tx *sql.Tx) error {
			cte, args, err := newPendingLogReader(tx).pageCTE(ctx, domain.RequestLogFilter{Limit: 25}, 26)
			if err != nil {
				return err
			}
			return drain(tx.QueryContext(ctx, "WITH "+cte[:len(cte)-2]+" SELECT request_id FROM pending_page", args...))
		})
	}})
	cold := func(b *testing.B) {
		b.Helper()
		b.StopTimer()
		if supported, err := dropPageCache(path, path+"-wal"); err != nil {
			b.Fatal(err)
		} else if !supported {
			b.Skip("the OS page cache cannot be dropped here")
		}
		b.StartTimer()
	}
	for _, test := range reads {
		for _, impl := range []struct {
			name string
			run  func() error
		}{{"before", test.before}, {"after", test.after}} {
			b.Run(fmt.Sprintf("%s/%s/warm", test.name, impl.name), func(b *testing.B) {
				if err := impl.run(); err != nil {
					b.Fatal(err)
				}
				b.ResetTimer()
				for range b.N {
					if err := impl.run(); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run(fmt.Sprintf("%s/%s/cold", test.name, impl.name), func(b *testing.B) {
				for range b.N {
					cold(b)
					if err := impl.run(); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
	for _, mode := range []string{"warm", "cold"} {
		b.Run("index_build/"+mode, func(b *testing.B) {
			for range b.N {
				b.StopTimer()
				if _, err := s.db.ExecContext(ctx, `DROP INDEX idx_usage_reservations_pending`); err != nil {
					b.Fatal(err)
				}
				if _, err := s.CheckpointWAL(ctx); err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				if mode == "cold" {
					cold(b)
				}
				if _, err := s.db.ExecContext(ctx, schemaV43); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
