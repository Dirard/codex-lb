package application_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

// The service folds recorded usage into the SQLite rollups until nothing
// lags, and the store's verifier and count check then find them consistent.
func TestUsageRollupServiceFoldsSQLiteUsage(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "codex-lb.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	for i := range 1500 {
		event := domain.UsageEvent{RequestID: fmt.Sprintf("usage-%d", i), Model: "gpt-test",
			Status: []string{"success", "error", "cancelled"}[i%3], RequestedAt: now.Add(-time.Duration(i) * time.Minute),
			ConversationID: fmt.Sprintf("conv-%d", i%17), TotalLatencyMS: int64(100 + i), FirstTokenMS: int64(10 + i%50),
			Usage: domain.UsageAmount{InputTokens: int64(i), OutputTokens: 7, CostMicrodollars: int64(3 * i)}}
		if _, err := store.RecordUsage(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	service := application.NewUsageRollupService(store, nil, nil)
	if err := service.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if stats := service.Stats(); stats != (application.UsageRollupStats{}) {
		t.Fatalf("stats after folding %+v", stats)
	}
	if check, err := store.VerifyUsageRollups(ctx, now); err != nil || !check.Checked || check.Mismatch != "" {
		t.Fatalf("verifier %+v %v", check, err)
	}
	if check, err := store.CheckUsageRollupCount(ctx); err != nil || !check.Checked || check.Mismatch != "" {
		t.Fatalf("count check %+v %v", check, err)
	}
	if fold, err := store.FoldUsageRollups(ctx, 10, 10); err != nil || fold.Rows != 0 || fold.Backlog {
		t.Fatalf("fold after the run %+v %v", fold, err)
	}
}
