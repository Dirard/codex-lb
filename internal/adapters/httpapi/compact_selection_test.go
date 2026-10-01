package httpapi_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/adapters/httpapi"
	"codex-lb/internal/adapters/provider"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
	"codex-lb/internal/domain/pricing"
)

type compactAdmissionNotice struct {
	proxy   *application.Proxy
	entered chan struct{}
}

func (a compactAdmissionNotice) Acquire(ctx context.Context) (func(), error) {
	close(a.entered)
	return a.proxy.Acquire(ctx)
}

func TestCompactSelectionSharesStrategyQuotaAndWaitsBeforeReadingCandidates(t *testing.T) {
	ctx := context.Background()
	var calls atomic.Int64
	selected := make(chan string, 8)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		selected <- r.Header.Get("ChatGPT-Account-ID")
		id := calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"compact_selected_%d","object":"response.compact","output":[{"type":"compaction","encrypted_content":"opaque"}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`, id)
	}))
	defer upstream.Close()
	_, store, proxy := wireFixtureWithProxy(t, nil)
	proxy.ResolvePrice = func(context.Context, domain.Account, string) (pricing.Price, error) { return pricing.Price{}, nil }
	account, err := store.GetAccount(ctx, "wire-account")
	if err != nil {
		t.Fatal(err)
	}
	credential, err := store.GetAccountCredential(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"wire-account", "z-selected"} {
		account.ID, account.ChatGPTAccountID, account.PlanType = id, id, "pro"
		if err := store.SaveAccount(ctx, account); err != nil {
			t.Fatal(err)
		}
		credential.AccountID = id
		if err := store.SaveAccountCredential(ctx, credential); err != nil {
			t.Fatal(err)
		}
	}
	settings, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.RoutingStrategy, settings.SingleAccountID = "single_account", "z-selected"
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	adapter := provider.New(store, fixedTokenSource{}, nil, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL + "/codex"})
	defer adapter.Close()
	operations := application.NewCodexOperations(store, store, adapter, time.Hour)
	operations.ConfigureAdmission(proxy)
	operations.ConfigureAccountSelection(proxy)
	operations.ConfigurePrice(proxy.ResolvePrice)
	mux := http.NewServeMux()
	httpapi.RegisterCodexOperationRoutes(mux, store, operations)
	server := httptest.NewServer(mux)
	defer server.Close()
	endpoint := server.URL + "/v1/responses/compact/"
	post := func(model, previous string) (int, string) {
		t.Helper()
		return liteHTTPPost(t, endpoint, fmt.Sprintf(`{"model":%q,"input":"history","previous_response_id":%q}`, model, previous))
	}
	if status, body := post("gpt-6-sol", ""); status != 200 {
		t.Fatalf("single-account compact failed: %d %s", status, body)
	}
	if got := <-selected; got != "z-selected" {
		t.Fatalf("compact ignored strategy: %s", got)
	}
	settings, err = store.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.RoutingStrategy, settings.SingleAccountID = "round_robin", ""
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"wire-account", "z-selected"} {
		now, reset := time.Now().UTC(), time.Now().Add(time.Hour)
		snapshot := domain.AccountUsageSnapshot{AccountID: id, ObservedAt: now, AdditionalReported: true,
			Quotas: []domain.AccountQuota{{AccountID: id, Window: "primary", UsedPercent: 100, ResetAt: &reset, ObservedAt: now}}}
		if id == "z-selected" {
			snapshot.AdditionalQuotas = []domain.AccountAdditionalQuota{{AccountID: id, QuotaKey: "codex_spark", LimitName: "codex_other", MeteredFeature: "codex_bengalfox", Window: "primary", UsedPercent: 0, ResetAt: &reset, ObservedAt: now}}
		}
		if err := store.SaveAccountUsageSnapshot(ctx, snapshot); err != nil {
			t.Fatal(err)
		}
	}
	if status, body := post("gpt-5.3-codex-spark", ""); status != 200 {
		t.Fatalf("fresh separate quota blocked by standard exhaustion: %d %s", status, body)
	}
	if got := <-selected; got != "z-selected" {
		t.Fatalf("compact picked missing additional quota: %s", got)
	}
	if err := store.SaveAccountUsageSnapshot(ctx, domain.AccountUsageSnapshot{AccountID: "z-selected", ObservedAt: time.Now().UTC(), AdditionalReported: true}); err != nil {
		t.Fatal(err)
	}
	if status, body := post("gpt-5.3-codex-spark", ""); status != 429 || !strings.Contains(body, "additional_quota_data_unavailable") || calls.Load() != 2 {
		t.Fatalf("missing additional quota dispatched compact: %d %s", status, body)
	}
	if status, body := post("gpt-5.3-codex-spark", "compact_selected_2"); status != 200 {
		t.Fatalf("established owner was telemetry-blocked: %d %s", status, body)
	}
	if got := <-selected; got != "z-selected" {
		t.Fatalf("telemetry moved established compact owner: %s", got)
	}

	// Make a new request eligible, occupy the only stream, then exhaust that
	// account while compact waits. Selection must observe the later snapshot.
	reset, now := time.Now().Add(time.Hour), time.Now().UTC()
	if err := store.SaveAccountQuota(ctx, domain.AccountQuota{AccountID: "z-selected", Window: "primary", UsedPercent: 0, ResetAt: &reset, ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	release, err := proxy.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	release = sync.OnceFunc(release)
	defer release()
	entered := make(chan struct{})
	operations.ConfigureAdmission(compactAdmissionNotice{proxy: proxy, entered: entered})
	type outcome struct {
		status int
		body   string
		err    error
	}
	finished := make(chan outcome, 1)
	go func() {
		r, _ := http.NewRequest("POST", endpoint, strings.NewReader(`{"model":"gpt-6-sol","input":"queued"}`))
		r.Header.Set("Authorization", "Bearer synthetic-key")
		r.Header.Set("Content-Type", "application/json")
		res, err := (&http.Client{Timeout: 5 * time.Second}).Do(r)
		if err != nil {
			finished <- outcome{err: err}
			return
		}
		body, readErr := io.ReadAll(res.Body)
		res.Body.Close()
		finished <- outcome{status: res.StatusCode, body: string(body), err: readErr}
	}()
	select {
	case <-entered:
	case result := <-finished:
		t.Fatalf("compact failed before admission: %+v", result)
	case <-time.After(5 * time.Second):
		t.Fatal("compact did not enter admission")
	}
	if err := store.SaveAccountQuota(ctx, domain.AccountQuota{AccountID: "z-selected", Window: "primary", UsedPercent: 100, ResetAt: &reset, ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	release()
	result := <-finished
	if result.err != nil || result.status != 503 || calls.Load() != 3 {
		t.Fatalf("compact selected stale pre-wait account or double-admitted: %+v calls=%d", result, calls.Load())
	}
	totals, err := store.UsageTotals(ctx, "wire-key", "")
	if err != nil || totals.RequestCount != 3 || totals.Usage.InputTokens != 6 {
		t.Fatalf("selection refusal was reserved/settled: %+v %v", totals, err)
	}
}
