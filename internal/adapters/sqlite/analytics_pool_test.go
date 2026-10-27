package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

// Dashboard analytics use their own bounded pool: saturating it delays only
// other analytics, never proxy authentication or continuation reads.
func TestAnalyticsCannotTakeProxyReadConnections(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	key := testKey("key", nil)
	if err := store.SaveAPIKey(ctx, key, fixedTime); err != nil {
		t.Fatal(err)
	}
	if got := store.analyticsDB.Stats().MaxOpenConnections; got != analyticsPoolSize {
		t.Fatalf("analytics pool size %d", got)
	}
	held := make([]*sql.Conn, 0, analyticsPoolSize)
	for range analyticsPoolSize {
		conn, err := store.analyticsDB.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, conn)
	}
	defer func() {
		for _, conn := range held {
			_ = conn.Close()
		}
	}()
	waiting, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if _, err := store.ListRequestLogs(waiting, domain.RequestLogFilter{Limit: 10}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("request logs did not use the saturated analytics pool: %v", err)
	}
	proxyRead, cancelProxy := context.WithTimeout(ctx, 2*time.Second)
	defer cancelProxy()
	if _, err := store.FindAPIKeyByHash(proxyRead, key.KeyHash); err != nil {
		t.Fatalf("proxy authentication waited for analytics: %v", err)
	}
	if _, err := store.GetContinuation(proxyRead, "key", "missing", time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("continuation read waited for analytics: %v", err)
	}
}
