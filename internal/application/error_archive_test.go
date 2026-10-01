package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

func TestErrorArchiveIsEncryptedRedactedAndNotCreatedForSuccess(t *testing.T) {
	dir := t.TempDir()
	store, err := sqlite.Open(filepath.Join(dir, "archive.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	vault, err := credentials.Open(filepath.Join(dir, "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	archives := application.NewErrorArchives(store, vault)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := store.SaveAccount(ctx, domain.Account{ID: "a", Kind: domain.AccountChatGPT, Status: domain.AccountActive, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	diagnostic := application.ErrorDiagnostic{RequestID: "r1", AccountID: "a", KeyID: "k", Model: "gpt-6-sol", OccurredAt: now, Status: "success", Request: json.RawMessage(`{"authorization":"Bearer synthetic-secret","nested":{"refresh_token":"refresh-secret"},"input":"visible error prompt with sk-synthetic12345"}`), Response: json.RawMessage(`{"cookie":"hidden-cookie","message":"upstream failed"}`)}
	if err := archives.RecordFailure(ctx, diagnostic); err != nil {
		t.Fatal(err)
	}
	query := domain.ErrorArchiveFilter{RequestID: "r1", Limit: 100}
	page, err := store.QueryErrorArchives(ctx, query, now)
	if err != nil || page.Total != 0 {
		t.Fatal("successful content archived")
	}
	diagnostic.Status = "error"
	diagnostic.ErrorCode = "connection_reset"
	if err := archives.RecordFailure(ctx, diagnostic); err != nil {
		t.Fatal(err)
	}
	if err := archives.RecordFailure(ctx, diagnostic); err != nil {
		t.Fatal(err)
	}
	page, err = store.QueryErrorArchives(ctx, query, now)
	if err != nil || page.Total != 1 {
		t.Fatal("error diagnostic not idempotent")
	}
	if strings.Contains(string(page.Items[0].ContentEncrypted), "visible error prompt") {
		t.Fatal("plaintext persisted")
	}
	_, contents, err := archives.Query(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(contents)
	for _, secret := range []string{"synthetic-secret", "refresh-secret", "hidden-cookie", "sk-synthetic12345"} {
		if strings.Contains(string(body), secret) {
			t.Fatal("sensitive structured field was not redacted")
		}
	}
	if !strings.Contains(string(body), "visible error prompt") {
		t.Fatal("diagnostic context lost")
	}
	if err := store.PruneErrorArchives(ctx, now.Add(8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	page, err = store.QueryErrorArchives(ctx, query, now)
	if err != nil || page.Total != 0 {
		t.Fatal("error archive did not expire")
	}
}

func TestProxyFailureArchivesButSuccessDoesNot(t *testing.T) {
	proxy, store, stub := proxyFixture(t)
	vault, err := credentials.Open(filepath.Join(t.TempDir(), "archive.key"), true)
	if err != nil {
		t.Fatal(err)
	}
	archives := application.NewErrorArchives(store, vault)
	proxy.Diagnostics = func(ctx context.Context, d application.ErrorDiagnostic) {
		if err := archives.RecordFailure(context.WithoutCancel(ctx), d); err != nil {
			t.Error(err)
		}
	}
	body := json.RawMessage(`{"model":"gpt-6-sol","input":"error-only prompt"}`)
	if _, err := proxy.Respond(context.Background(), application.ResponseOptions{KeyID: "key-test"}, body, nil); err != nil {
		t.Fatal(err)
	}
	start := time.Now().Add(-time.Minute)
	page, _, err := archives.Query(context.Background(), domain.ErrorArchiveFilter{Start: &start, Limit: 100})
	if err != nil || page.Total != 0 {
		t.Fatal("success archived by proxy")
	}
	stub.respond = func(context.Context, application.ResponseTarget, json.RawMessage, func(application.ResponseEvent) error) (application.ResponseResult, error) {
		return application.ResponseResult{}, errors.New("synthetic transport failure")
	}
	if _, err := proxy.Respond(context.Background(), application.ResponseOptions{KeyID: "key-test"}, body, nil); err == nil {
		t.Fatal("failure swallowed")
	}
	page, contents, err := archives.Query(context.Background(), domain.ErrorArchiveFilter{Start: &start, Limit: 100})
	if err != nil || page.Total != 1 || !strings.Contains(string(contents[0].Request), "error-only prompt") {
		t.Fatal("proxy error missing diagnostic")
	}
	totals, err := store.UsageTotals(context.Background(), "key-test", "")
	pending, pendingErr := store.ListReservationsNeedingReconciliation(context.Background(), "", 10)
	if err != nil || totals.RequestCount != 1 || totals.FailedCount != 0 || pendingErr != nil || len(pending) != 1 {
		t.Fatal("uncertain failure was zero-settled instead of retained")
	}
}

func TestDiagnosticEventAndBodyBounds(t *testing.T) {
	var events application.DiagnosticEvents
	for range 100 {
		events.Add(application.ResponseEvent{Data: json.RawMessage(`{"type":"response.output_text.delta","delta":"event"}`)})
	}
	if len(events.Events) != 64 || !events.Truncated {
		t.Fatal("unbounded event diagnostics")
	}
	before := len(events.Events)
	events.Add(application.ResponseEvent{Data: json.RawMessage(strings.Repeat("x", 2<<20))})
	if len(events.Events) != before {
		t.Fatal("oversized event retained")
	}
}
