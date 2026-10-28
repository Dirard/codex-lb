package sqlite

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

// DB-19: counter-based eviction keeps the same newest prefix as the former
// window function and the counters stay equal to the table contents.
func TestErrorArchiveCountersEvictOldestAndStayConsistent(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	vault, err := credentials.Open(filepath.Join(t.TempDir(), "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	sizes := []int{900, 100, 2000, 50, 700, 1300, 40, 600}
	started := time.Now().Add(-time.Hour)
	var lengths []int
	const maxBytes = 4000
	for i, size := range sizes {
		encrypted, err := vault.Encrypt([]byte(strings.Repeat("x", size)))
		if err != nil {
			t.Fatal(err)
		}
		lengths = append(lengths, len(encrypted))
		entry := domain.ErrorArchive{RequestID: fmt.Sprintf("r%d", i), OccurredAt: started.Add(time.Duration(i) * time.Second),
			ExpiresAt: time.Now().Add(time.Hour), ContentEncrypted: encrypted}
		if err := store.SaveErrorArchive(ctx, entry, maxBytes); err != nil {
			t.Fatal(err)
		}
	}
	// Expected: the newest archives whose cumulative size fits maxBytes.
	kept, total := 0, 0
	for i := len(lengths) - 1; i >= 0 && total+lengths[i] <= maxBytes; i-- {
		total += lengths[i]
		kept++
	}
	checkCounters := func(wantCount int) {
		t.Helper()
		var count, bytes, statsCount, statsBytes int
		if err := store.readDB.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(length(content_encrypted)),0) FROM error_archives`).Scan(&count, &bytes); err != nil {
			t.Fatal(err)
		}
		if err := store.readDB.QueryRowContext(ctx, `SELECT archive_count,archive_bytes FROM error_archive_stats`).Scan(&statsCount, &statsBytes); err != nil {
			t.Fatal(err)
		}
		if count != wantCount || statsCount != count || statsBytes != bytes {
			t.Fatalf("archives=%d/%d bytes=%d/%d want %d", count, statsCount, bytes, statsBytes, wantCount)
		}
	}
	checkCounters(kept)
	var oldest string
	if err := store.readDB.QueryRowContext(ctx, `SELECT request_id FROM error_archives ORDER BY occurred_at LIMIT 1`).Scan(&oldest); err != nil ||
		oldest != fmt.Sprintf("r%d", len(sizes)-kept) {
		t.Fatalf("oldest kept = %q %v", oldest, err)
	}
	if err := store.PruneErrorArchives(ctx, time.Now().Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	checkCounters(0)
}

func TestCodexResourceOwnerCounterTracksInsertsAndDeletes(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	saveTestAccount(t, store, "acct")
	if err := store.SaveAPIKey(ctx, testKey("key", nil), fixedTime); err != nil {
		t.Fatal(err)
	}
	account, err := store.GetAccount(ctx, "acct")
	if err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		owner := application.CodexResourceOwner{ResourceType: application.CodexResourceFile, ResourceID: fmt.Sprintf("file-%d", i),
			KeyID: "key", AccountID: "acct", AccountGeneration: account.Generation, RouteRevision: account.RouteRevision,
			ExpiresAt: time.Now().Add(time.Hour)}
		if err := store.SaveCodexResourceOwner(ctx, owner); err != nil {
			t.Fatal(err)
		}
		// Refreshing an existing owner is an update, not a second row.
		if err := store.SaveCodexResourceOwner(ctx, owner); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM codex_resource_owners WHERE resource_id='file-0'`); err != nil {
		t.Fatal(err)
	}
	var rows, counted int
	if err := store.readDB.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM codex_resource_owners),(SELECT owner_count FROM codex_resource_owner_stats)`).Scan(&rows, &counted); err != nil || rows != 4 || counted != 4 {
		t.Fatalf("owners=%d counter=%d err=%v", rows, counted, err)
	}
}

// DB-20: a job and its runs disappear together.
func TestDeleteAutomationJobRemovesRunsAtomically(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	job := domain.AutomationJob{ID: "job", Name: "w", Enabled: true,
		Schedule: domain.AutomationSchedule{Type: "daily", Time: "03:00", Timezone: "UTC", Days: []string{"mon"}},
		Model:    "m", Prompt: "ping", CreatedAt: fixedTime, UpdatedAt: fixedTime}
	if err := store.SaveAutomationJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if err := store.EnsureAutomationRun(ctx, domain.AutomationRun{ID: fmt.Sprintf("run-%d", i), JobID: "job",
			CycleKey: "cycle", SlotKey: fmt.Sprintf("slot-%d", i), Trigger: domain.AutomationTriggerScheduled,
			Model: "m", ScheduledFor: fixedTime}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.DeleteAutomationJob(ctx, "job"); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, store, `SELECT count(*) FROM automation_runs`); n != 0 {
		t.Fatalf("orphaned runs = %d", n)
	}
	if err := store.DeleteAutomationJob(ctx, "job"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete = %v", err)
	}
}

// DB-21: releasing a reservation after a downward reprice clamps at zero
// instead of leaving the reserved amount charged.
func TestReleaseAfterDownwardRepriceClampsLimitAtZero(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	saveTestAccount(t, s, "acct")
	key := testKey("key", nil)
	key.Limits = []domain.LimitRule{tokenLimit(1000)}
	if err := s.SaveAPIKey(ctx, key, fixedTime); err != nil {
		t.Fatal(err)
	}
	reserve := domain.ReservationRequest{ID: "reservation", APIKeyID: "key", AccountID: "acct", Model: "gpt-test",
		Budget: domain.UsageAmount{InputTokens: 40}, Now: fixedTime}
	if _, err := s.ReserveUsage(ctx, reserve); err != nil {
		t.Fatal(err)
	}
	// A reprice of the same window lowered consumption below the reserved amount.
	if _, err := s.db.ExecContext(ctx, `UPDATE api_key_limits SET current_value=10 WHERE api_key_id='key'`); err != nil {
		t.Fatal(err)
	}
	if applied, err := s.SettleUsage(ctx, reserve.ID, domain.UsageSettlement{Status: "released"}); err != nil || !applied {
		t.Fatalf("release = %v %v", applied, err)
	}
	loaded, err := s.GetAPIKey(ctx, "key")
	if err != nil || loaded.Limits[0].CurrentValue != 0 {
		t.Fatalf("limit after release = %+v %v", loaded.Limits, err)
	}
}
