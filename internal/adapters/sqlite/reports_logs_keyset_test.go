package sqlite

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"codex-lb/internal/domain"
)

func TestRequestLogKeysetPagesMatchOffsetPages(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	if err := s.SaveAPIKey(ctx, testKey("key", nil), fixedTime); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i := range 230 {
		// Equal timestamps exercise the request_id tie-breaker.
		at := millis(fixedTime) - int64(i/3)*1000
		var legacy any
		if i%10 == 0 {
			legacy = fmt.Sprintf("legacy-%03d", i)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO usage_events(request_id,api_key_id,account_id,model,status,requested_at,legacy_request_id)
 VALUES(?,?,?,?,?,?,?)`, fmt.Sprintf("req-%03d", i), "key", "acct", "model", "success", at, legacy); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 4 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO usage_reservations(id,api_key_id,account_id,model,status,created_at,updated_at,needs_reconciliation)
 VALUES(?,'key','acct','model','reserved',?,?,1)`, fmt.Sprintf("pending-%d", i), millis(fixedTime)-int64(i)*30000, millis(fixedTime)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var cursor *domain.RequestLogCursor
	pages := 0
	for offset := 0; ; offset += 25 {
		byOffset, err := s.ListRequestLogs(ctx, domain.RequestLogFilter{Limit: 25, Offset: offset})
		if err != nil {
			t.Fatal(err)
		}
		byCursor, err := s.ListRequestLogs(ctx, domain.RequestLogFilter{Limit: 25, Before: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(byOffset.Requests, byCursor.Requests) || byOffset.Total != 234 || byCursor.Total != 234 {
			t.Fatalf("page %d differs or totals wrong: offset total=%d cursor total=%d", pages, byOffset.Total, byCursor.Total)
		}
		if byOffset.HasMore != byCursor.HasMore || (byCursor.NextCursor != nil) != byCursor.HasMore {
			t.Fatalf("page %d hasMore offset=%v cursor=%v next=%v", pages, byOffset.HasMore, byCursor.HasMore, byCursor.NextCursor)
		}
		pages++
		if !byCursor.HasMore {
			break
		}
		next, err := domain.ParseRequestLogCursor(*byCursor.NextCursor)
		if err != nil {
			t.Fatal(err)
		}
		cursor = &next
	}
	if pages != 10 {
		t.Fatalf("pages = %d", pages)
	}
	if _, err := domain.ParseRequestLogCursor("not a cursor"); err == nil {
		t.Fatal("malformed cursor accepted")
	}
}

func TestRequestLogPageUsesTimeIndexWithoutSorting(t *testing.T) {
	s, _ := testStore(t)
	events, pending, args := requestLogSources(domain.RequestLogFilter{Limit: 25})
	rows, err := s.readDB.Query("EXPLAIN QUERY PLAN "+requestLogPageSource(events, pending)+"SELECT request_id FROM log_page", append(args, 26, 0)...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	plan := ""
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan += detail + "\n"
	}
	if !strings.Contains(plan, "idx_usage_events_time_id") || strings.Contains(plan, "SCAN e\n") {
		t.Fatalf("request-log page plan:\n%s", plan)
	}
	rows2, err := s.readDB.Query(`EXPLAIN QUERY PLAN SELECT id FROM usage_reservations WHERE needs_reconciliation=1 AND status='reserved' AND id>? ORDER BY id LIMIT 10`, "")
	if err != nil {
		t.Fatal(err)
	}
	defer rows2.Close()
	plan = ""
	for rows2.Next() {
		var id, parent, unused int
		var detail string
		if err := rows2.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan += detail + "\n"
	}
	if !strings.Contains(plan, "idx_usage_reservations_open") || strings.Contains(plan, "TEMP B-TREE") {
		t.Fatalf("open reservation plan:\n%s", plan)
	}
}
