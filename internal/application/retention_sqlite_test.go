package application_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/application"
)

// A clock with sub-millisecond precision used to leave an empty final
// thinning chunk that the store rejected, failing every retention cycle.
func TestRetentionCycleOnAFreshStoreWithSubMillisecondClock(t *testing.T) {
	store, err := sqlite.Open("file:" + filepath.Join(t.TempDir(), "retention.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 10, 28, 6, 30, 57, 893456789, time.UTC)
	service := application.NewRetentionService(store, func() time.Time { return now })
	for cycle := range 2 {
		result, err := service.RunCycle(context.Background(), 20*time.Second)
		if err != nil || len(result.Failed) != 0 || result.Backlog {
			t.Fatalf("cycle %d: result=%+v err=%v", cycle, result, err)
		}
		now = now.Add(time.Hour + 123456*time.Nanosecond)
	}
}
