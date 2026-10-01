package httpapi_test

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/adapters/httpapi"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

func Test256ConcurrentResponsesSSEStreamsSettleAndRelease(t *testing.T) {
	_, store, _ := wireFixtureWithProxy(t, nil)
	ctx := context.Background()
	credential, err := store.GetAccountCredential(ctx, "wire-account")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for i := 1; i < 40; i++ {
		id := fmt.Sprintf("capacity-account-%02d", i)
		if err := store.SaveAccount(ctx, domain.Account{ID: id, Kind: domain.AccountChatGPT, Provider: "openai",
			Email: id + "@example.invalid", PlanType: "plus", Status: domain.AccountActive, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
		credential.AccountID = id
		if err := store.SaveAccountCredential(ctx, credential); err != nil {
			t.Fatal(err)
		}
	}
	keys := make([]string, 256)
	keys[0] = "synthetic-key"
	for i := 1; i < len(keys); i++ {
		keys[i] = fmt.Sprintf("capacity-key-%03d", i)
		id := fmt.Sprintf("capacity-id-%03d", i)
		if err := store.SaveAPIKey(ctx, domain.APIKey{ID: id, Name: id,
			KeyHash: fmt.Sprintf("%x", sha256.Sum256([]byte(keys[i]))), KeyPrefix: "capacity",
			IsActive: true, CreatedAt: now,
			Limits: []domain.LimitRule{{Type: domain.LimitTotalTokens, Window: domain.WindowWeekly, MaxValue: 100000}}}, now); err != nil {
			t.Fatal(err)
		}
	}
	settings, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	create, stream, reserve, fairShare := 4, 8, 1, 0
	settings.RoutingStrategy = "round_robin"
	settings.ProxyAccountResponseCreateLimitOverride = &create
	settings.ProxyAccountStreamLimitOverride = &stream
	settings.ProxyAccountStreamRecoveryReserveOverride = &reserve
	settings.ProxyApiKeyFairShareCongestionThresholdPctOverride = &fairShare
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	vault, err := credentials.Open(filepath.Join(t.TempDir(), "capacity-key"), true)
	if err != nil {
		t.Fatal(err)
	}
	barrier := make(chan struct{})
	var release sync.Once
	openBarrier := func() { release.Do(func() { close(barrier) }) }
	cancelled := make(chan struct{}, len(keys))
	var nextID, active, peak atomic.Int64
	provider := wireProvider(func(ctx context.Context, target application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		id := fmt.Sprintf("capacity-response-%03d", nextID.Add(1))
		n := active.Add(1)
		defer active.Add(-1)
		for previous := peak.Load(); n > previous && !peak.CompareAndSwap(previous, n); previous = peak.Load() {
		}
		if target.OnFirstUpstreamEvent != nil {
			target.OnFirstUpstreamEvent()
		}
		first := application.ResponseEvent{Type: "response.created", Data: []byte(fmt.Sprintf(`{"type":"response.created","response":{"id":%q,"status":"in_progress"}}`, id))}
		if err := emit(first); err != nil {
			if ctx.Err() != nil {
				cancelled <- struct{}{}
			}
			return application.ResponseResult{}, err
		}
		// The proxy holds response.created until content arrives, then flushes both.
		if err := emit(application.ResponseEvent{Type: "response.output_text.delta", Data: []byte(`{"type":"response.output_text.delta","delta":"ready"}`)}); err != nil {
			if ctx.Err() != nil {
				cancelled <- struct{}{}
			}
			return application.ResponseResult{}, err
		}
		select {
		case <-ctx.Done():
			cancelled <- struct{}{}
			return application.ResponseResult{}, ctx.Err()
		case <-barrier:
			return wireComplete(id, emit)
		}
	})
	proxy := application.NewProxy(store, provider, vault, application.ProxyConfig{})
	server := httptest.NewServer(httpapi.NewProxyHandler(store, proxy, nil))
	defer server.Close()
	defer openBarrier()
	client := server.Client()
	client.Timeout = 60 * time.Second
	opened := make(chan error, len(keys))
	finished := make(chan error, len(keys))
	cancels := make([]context.CancelFunc, len(keys))
	for i, token := range keys {
		requestCtx, cancel := context.WithCancel(ctx)
		cancels[i] = cancel
		go func(i int, token string) {
			request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, server.URL+"/v1/responses",
				strings.NewReader(`{"model":"gpt-6-sol","input":"hello","stream":true}`))
			if err != nil {
				opened <- err
				finished <- err
				return
			}
			request.Header.Set("Authorization", "Bearer "+token)
			request.Header.Set("Content-Type", "application/json")
			response, err := client.Do(request)
			if err != nil {
				opened <- err
				finished <- err
				return
			}
			defer response.Body.Close()
			reader := bufio.NewReader(response.Body)
			line, readErr := reader.ReadString('\n')
			if response.StatusCode != http.StatusOK || readErr != nil || line != "event: response.created\n" {
				body, _ := io.ReadAll(reader)
				err = fmt.Errorf("first SSE event: status=%d line=%q body=%s err=%v", response.StatusCode, line, body, readErr)
				opened <- err
				finished <- err
				return
			}
			opened <- nil
			body, readErr := io.ReadAll(reader)
			if i < 10 { // These clients are cancelled only after all 256 streams start.
				finished <- nil
				return
			}
			if readErr != nil || !strings.Contains(string(body), "response.completed") {
				finished <- fmt.Errorf("stream %d did not complete: %s %v", i, body, readErr)
				return
			}
			finished <- nil
		}(i, token)
	}
	defer func() {
		for _, cancel := range cancels {
			cancel()
		}
	}()
	deadline := time.After(45 * time.Second)
	for range keys {
		select {
		case err := <-opened:
			if err != nil {
				t.Fatalf("256 active SSE streams were not admitted: peak=%d: %v", peak.Load(), err)
			}
		case <-deadline:
			t.Fatalf("256 active SSE streams timed out: peak=%d", peak.Load())
		}
	}
	if peak.Load() < 256 {
		t.Fatalf("only %d upstream streams active", peak.Load())
	}
	for _, cancel := range cancels[:10] {
		cancel()
	}
	for range 10 {
		select {
		case <-cancelled:
		case <-deadline:
			t.Fatal("cancelled stream did not release upstream work")
		}
	}
	openBarrier()
	for range keys {
		select {
		case err := <-finished:
			if err != nil {
				t.Fatal(err)
			}
		case <-deadline:
			t.Fatal("stream clients did not finish")
		}
	}
	for until := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		pending, err := store.ListReservationsNeedingReconciliation(ctx, "", 1000)
		if err != nil {
			t.Fatal(err)
		}
		totals, err := store.UsageTotals(ctx, "", "")
		if err != nil {
			t.Fatal(err)
		}
		if len(pending) == 10 && totals.RequestCount == 246 && active.Load() == 0 {
			break
		}
		if time.Now().After(until) {
			t.Fatalf("accounting or resources leaked: pending=%d settled=%d active=%d", len(pending), totals.RequestCount, active.Load())
		}
	}
	held, err := store.GetAPIKey(ctx, "wire-key")
	if err != nil || held.Limits[0].CurrentValue <= 20 {
		t.Fatalf("cancelled request lost held budget: %+v %v", held.Limits, err)
	}
	settled, err := store.GetAPIKey(ctx, "capacity-id-010")
	if err != nil || settled.Limits[0].CurrentValue != 20 {
		t.Fatalf("completed request settled wrong budget: %+v %v", settled.Limits, err)
	}
}
