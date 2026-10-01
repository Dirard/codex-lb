package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

type filePinOperationsProvider struct{ createdOn string }

func (p *filePinOperationsProvider) Compact(context.Context, application.CodexOperationTarget, json.RawMessage) (application.CodexOperationResult, error) {
	return application.CodexOperationResult{}, nil
}
func (p *filePinOperationsProvider) Control(context.Context, application.CodexOperationTarget, application.CodexControlRequest) (application.CodexOperationResult, error) {
	return application.CodexOperationResult{}, nil
}
func (p *filePinOperationsProvider) CreateFile(_ context.Context, target application.CodexOperationTarget, _ json.RawMessage) (application.CodexOperationResult, error) {
	p.createdOn = target.Account.ID
	return application.CodexOperationResult{Status: 200, Body: []byte(`{"file_id":"file-123"}`)}, nil
}
func (p *filePinOperationsProvider) FinalizeFile(context.Context, application.CodexOperationTarget, string) (application.CodexOperationResult, error) {
	return application.CodexOperationResult{}, nil
}
func (p *filePinOperationsProvider) Transcribe(context.Context, application.CodexOperationTarget, application.CodexTranscriptionRequest) (application.CodexOperationResult, error) {
	return application.CodexOperationResult{}, nil
}
func (p *filePinOperationsProvider) Realtime(context.Context, application.CodexOperationTarget,
	application.CodexRealtimeRequest, application.CodexRealtimeConnection) error {
	return nil
}

type filePinResponseProvider struct {
	accounts []string
	quota    bool
}

func (p *filePinResponseProvider) Respond(_ context.Context, target application.ResponseTarget, _ json.RawMessage, _ func(application.ResponseEvent) error) (application.ResponseResult, error) {
	p.accounts = append(p.accounts, target.Account.ID)
	if p.quota {
		return application.ResponseResult{Failed: true, ErrorCode: "insufficient_quota"},
			&application.ProviderFailure{Code: "insufficient_quota", Status: 429, QuotaRefused: true, Dispatched: true}
	}
	id := fmt.Sprintf("resp-%d", len(p.accounts))
	response := json.RawMessage(fmt.Sprintf(`{"id":%q,"status":"completed","output":[]}`, id))
	return application.ResponseResult{ResponseID: id, Response: response, Usage: domain.UsageAmount{InputTokens: 10, OutputTokens: 5}, UsageKnown: true}, nil
}

func TestCreatedFilePinsResponsesToOwnerAcrossRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "store.sqlite")
	store, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	vault, err := credentials.Open(filepath.Join(dir, "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := vault.Encrypt([]byte("synthetic-token"))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"account-a", "account-b"} {
		if err := store.SaveAccount(ctx, domain.Account{ID: id, Kind: domain.AccountChatGPT, Provider: "openai",
			Email: id + "@example.invalid", PlanType: "plus", Status: domain.AccountActive,
			RoutingPolicy: "normal", CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveAccountCredential(ctx, domain.AccountCredential{AccountID: id,
			AccessTokenEncrypted: cipher, RefreshTokenEncrypted: cipher, IDTokenEncrypted: cipher}); err != nil {
			t.Fatal(err)
		}
	}
	plainKey := "sk-clb-test-file-pin"
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(plainKey)))
	if err := store.SaveAPIKey(ctx, domain.APIKey{ID: "key-a", Name: "A", KeyHash: hash, KeyPrefix: "sk-clb-test",
		IsActive: true, CreatedAt: time.Now()}, time.Now()); err != nil {
		t.Fatal(err)
	}
	operations := &filePinOperationsProvider{}
	ops := application.NewCodexOperations(store, store, operations, time.Hour)
	ops.ConfigureAdmission(application.NewProxy(store, &filePinResponseProvider{}, vault, application.ProxyConfig{}))
	files := NewCodexOperationsHandler(store, ops)
	request := func(handler http.Handler, path, key string, body []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://localhost"+path, bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+key)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	created := request(files, "/backend-api/files", plainKey, []byte(`{"file_name":"a.txt","file_size":5,"use_case":"assistants"}`))
	if created.Code != 200 || operations.createdOn != "account-a" {
		t.Fatalf("file create: %d, %s", created.Code, operations.createdOn)
	}
	owner, err := store.GetCodexResourceOwner(ctx, "file", "file-123", "key-a", time.Now())
	if err != nil || owner.AccountID != "account-a" {
		t.Fatalf("file owner not persisted: %+v, %v", owner, err)
	}
	settings, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.RoutingStrategy = "single_account"
	settings.SingleAccountID = "account-b"
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	quotaReset := time.Now().Add(time.Hour)
	if err := store.SaveAccountQuota(ctx, domain.AccountQuota{AccountID: "account-a", Window: "primary",
		UsedPercent: 100, ResetAt: &quotaReset, ObservedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	responseProvider := &filePinResponseProvider{}
	proxy := application.NewProxy(store, responseProvider, vault, application.ProxyConfig{})
	proxyHandler := NewProxyHandler(store, proxy, nil)
	fileBody := []byte(`{"model":"gpt-6-sol","input":[{"type":"message","role":"user","content":[{"type":"input_file","file_id":"file-123"}]}]}`)
	got := request(proxyHandler, "/v1/responses", plainKey, fileBody)
	if got.Code != 200 || len(responseProvider.accounts) != 1 || responseProvider.accounts[0] != "account-a" {
		t.Fatalf("file-pinned response switched owner: %d, %+v, %s", got.Code, responseProvider.accounts, got.Body.String())
	}
	unknown := []byte(`{"model":"gpt-6-sol","input":[{"type":"input_file","file_id":"missing"}]}`)
	if got := request(proxyHandler, "/v1/responses", plainKey, unknown); got.Code != 409 || len(responseProvider.accounts) != 1 {
		t.Fatalf("unknown file dispatched: %d, %+v", got.Code, responseProvider.accounts)
	}
	otherKey := "sk-clb-other-file-pin"
	otherHash := fmt.Sprintf("%x", sha256.Sum256([]byte(otherKey)))
	if err := store.SaveAPIKey(ctx, domain.APIKey{ID: "key-b", Name: "B", KeyHash: otherHash,
		KeyPrefix: "sk-clb-other", IsActive: true, CreatedAt: time.Now()}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := request(proxyHandler, "/v1/responses", otherKey, fileBody); got.Code != 409 || len(responseProvider.accounts) != 1 {
		t.Fatalf("cross-key file dispatched: %d, %+v", got.Code, responseProvider.accounts)
	}
	if err := store.SaveCodexResourceOwner(ctx, application.CodexResourceOwner{ResourceType: "file",
		ResourceID: "file-other", KeyID: "key-a", AccountID: "account-b", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	multi := []byte(`{"model":"gpt-6-sol","input":[{"type":"input_file","file_id":"file-123"},{"type":"input_file","file_id":"file-other"}]}`)
	if got := request(proxyHandler, "/v1/responses", plainKey, multi); got.Code != 409 || len(responseProvider.accounts) != 1 {
		t.Fatalf("mixed file owners dispatched: %d, %+v", got.Code, responseProvider.accounts)
	}
	if got := request(proxyHandler, "/v1/responses", plainKey, []byte(`{"model":"gpt-6-sol","input":"fresh"}`)); got.Code != 200 || len(responseProvider.accounts) != 2 || responseProvider.accounts[1] != "account-b" {
		t.Fatalf("fresh route did not select configured account: %d, %+v", got.Code, responseProvider.accounts)
	}
	mismatch := []byte(`{"model":"gpt-6-sol","previous_response_id":"resp-2","input":[{"type":"input_file","file_id":"file-123"}]}`)
	if got := request(proxyHandler, "/v1/responses", plainKey, mismatch); got.Code != 409 || len(responseProvider.accounts) != 2 {
		t.Fatalf("conversation/file owner conflict dispatched: %d, %+v", got.Code, responseProvider.accounts)
	}
	quotaProvider := &filePinResponseProvider{quota: true}
	quotaProxy := application.NewProxy(store, quotaProvider, vault, application.ProxyConfig{})
	if got := request(NewProxyHandler(store, quotaProxy, nil), "/v1/responses", plainKey, fileBody); got.Code != 409 || len(quotaProvider.accounts) != 1 || quotaProvider.accounts[0] != "account-a" {
		t.Fatalf("quota-refused pinned file failed over: %d, %+v", got.Code, quotaProvider.accounts)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	responseProvider = &filePinResponseProvider{}
	proxy = application.NewProxy(reopened, responseProvider, vault, application.ProxyConfig{})
	if got := request(NewProxyHandler(reopened, proxy, nil), "/v1/responses", plainKey, fileBody); got.Code != 200 ||
		len(responseProvider.accounts) != 1 || responseProvider.accounts[0] != "account-a" {
		t.Fatalf("restart lost file pin: %d, %+v", got.Code, responseProvider.accounts)
	}
	key, err := reopened.GetAPIKey(ctx, "key-a")
	if err != nil {
		t.Fatal(err)
	}
	key.AccountAssignmentScopeEnabled = true
	key.AssignedAccountIDs = []string{"account-b"}
	if err := reopened.SaveAPIKey(ctx, key, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := request(NewProxyHandler(reopened, proxy, nil), "/v1/responses", plainKey, fileBody); got.Code != 409 || len(responseProvider.accounts) != 1 {
		t.Fatalf("revoked file owner scope dispatched: %d, %+v", got.Code, responseProvider.accounts)
	}
}
