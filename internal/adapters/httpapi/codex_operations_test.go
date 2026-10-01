package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

type operationsKeyStore struct {
	application.ProxyStore
	application.ModelSourceRepository

	keys     map[string]domain.APIKey
	accounts []domain.Account
	events   []domain.UsageEvent
}

func (s *operationsKeyStore) FindAPIKeyByHash(_ context.Context, hash string) (domain.APIKey, error) {
	for _, key := range s.keys {
		if key.KeyHash == hash {
			return key, nil
		}
	}
	return domain.APIKey{}, domain.ErrNotFound
}
func (s *operationsKeyStore) GetAPIKey(_ context.Context, id string) (domain.APIKey, error) {
	key, ok := s.keys[id]
	if !ok {
		return domain.APIKey{}, domain.ErrNotFound
	}
	return key, nil
}
func (s *operationsKeyStore) EligibleAccounts(context.Context, string) ([]domain.Account, error) {
	return s.accounts, nil
}
func (s *operationsKeyStore) QuotaCandidateAccounts(context.Context, string) ([]domain.Account, error) {
	return s.accounts, nil
}
func (s *operationsKeyStore) LoadAccountCreditStatus(context.Context, string) (*domain.AccountCreditStatus, error) {
	return nil, nil
}
func (s *operationsKeyStore) ListAccountQuota(context.Context, string) ([]domain.AccountQuota, error) {
	return nil, nil
}
func (s *operationsKeyStore) LoadSettings(context.Context) (domain.RuntimeSettings, error) {
	return domain.RuntimeSettings{RoutingStrategy: "round_robin"}, nil
}
func (s *operationsKeyStore) GetContinuation(context.Context, string, string, time.Time) (domain.Continuation, error) {
	return domain.Continuation{}, domain.ErrNotFound
}
func (s *operationsKeyStore) ListModelSources(context.Context) ([]domain.ModelSource, error) {
	return nil, nil
}
func (s *operationsKeyStore) ScopedAccountForOwner(_ context.Context, _, accountID string, _ time.Time) (domain.Account, error) {
	for _, account := range s.accounts {
		if account.ID == accountID {
			return account, nil
		}
	}
	return domain.Account{}, domain.ErrNotFound
}
func (s *operationsKeyStore) ReserveUsage(_ context.Context, request domain.ReservationRequest) (domain.Reservation, error) {
	return domain.Reservation{ID: request.ID, APIKeyID: request.APIKeyID, AccountID: request.AccountID, Model: request.Model}, nil
}
func (s *operationsKeyStore) SettleUsage(_ context.Context, _ string, settlement domain.UsageSettlement) (bool, error) {
	s.events = append(s.events, settlement.Event)
	return true, nil
}
func (s *operationsKeyStore) RecordCodexContentFreeUsage(_ context.Context, event domain.UsageEvent) error {
	s.events = append(s.events, event)
	return nil
}
func (s *operationsKeyStore) RecordAccountOutcome(context.Context, string, string, bool, bool) error {
	return nil
}

type operationsProvider struct {
	targets []application.CodexOperationTarget
}

func (p *operationsProvider) Compact(context.Context, application.CodexOperationTarget, json.RawMessage) (application.CodexOperationResult, error) {
	return application.CodexOperationResult{Status: 200, UsageKnown: true, Body: []byte(`{"object":"response.compact"}`), ContentType: "application/json"}, nil
}
func (p *operationsProvider) Control(_ context.Context, target application.CodexOperationTarget, _ application.CodexControlRequest) (application.CodexOperationResult, error) {
	p.targets = append(p.targets, target)
	return application.CodexOperationResult{Status: 200, Body: []byte(`{"ok":true}`), ContentType: "application/json"}, nil
}
func (p *operationsProvider) CreateFile(context.Context, application.CodexOperationTarget, json.RawMessage) (application.CodexOperationResult, error) {
	return application.CodexOperationResult{Status: 200, Body: []byte(`{"file_id":"file-1"}`), ContentType: "application/json"}, nil
}
func (p *operationsProvider) FinalizeFile(context.Context, application.CodexOperationTarget, string) (application.CodexOperationResult, error) {
	return application.CodexOperationResult{Status: 200, Body: []byte(`{"status":"success"}`), ContentType: "application/json"}, nil
}
func (p *operationsProvider) Transcribe(_ context.Context, target application.CodexOperationTarget, _ application.CodexTranscriptionRequest) (application.CodexOperationResult, error) {
	p.targets = append(p.targets, target)
	return application.CodexOperationResult{Status: 200, UsageKnown: true, Body: []byte(`{"text":"ok"}`), ContentType: "application/json"}, nil
}
func (p *operationsProvider) Realtime(_ context.Context, target application.CodexOperationTarget, _ application.CodexRealtimeRequest, _ application.CodexRealtimeConnection) error {
	p.targets = append(p.targets, target)
	return nil
}

func TestCodexOperationsRoutesAuthenticateAndDispatch(t *testing.T) {
	secret := "synthetic-proxy-key"
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(secret)))
	key := domain.APIKey{ID: "key", KeyHash: hash, IsActive: true, CreatedAt: time.Now()}
	account := domain.Account{ID: "acct", Kind: domain.AccountChatGPT, Provider: "openai", Status: domain.AccountActive, CreatedAt: time.Now()}
	store := &operationsKeyStore{keys: map[string]domain.APIKey{"key": key}, accounts: []domain.Account{account}}
	provider := &operationsProvider{}
	service := application.NewCodexOperations(store, &codexOwnerStore{}, provider, time.Hour)
	service.ConfigureAdmission(codexTestAdmission{})
	service.ConfigureAccountSelection(application.NewProxy(store, nil, nil, application.ProxyConfig{}))
	handler := NewCodexOperationsHandler(store, service)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })

	request := func(method, path string, body []byte, contentType string, auth bool) *httptest.ResponseRecorder {
		t.Helper()
		var reader *bytes.Reader
		if body == nil {
			reader = bytes.NewReader(nil)
		} else {
			reader = bytes.NewReader(body)
		}
		r := httptest.NewRequest(method, "http://localhost"+path, reader)
		if contentType != "" {
			r.Header.Set("Content-Type", contentType)
		}
		if auth {
			r.Header.Set("Authorization", "Bearer "+secret)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	if request("POST", "/backend-api/codex/responses/compact", []byte(`{}`), "application/json", false).Code != http.StatusUnauthorized {
		t.Fatal("missing authentication was accepted")
	}
	if request("GET", "/health", nil, "", false).Code != http.StatusNoContent {
		t.Fatal("handler did not compose with the runtime mux")
	}
	compact := request("POST", "/backend-api/codex/responses/compact", []byte(`{"model":"gpt-6-sol","instructions":"","input":"x"}`), "application/json", true)
	if compact.Code != http.StatusOK || compact.Header().Get("Cache-Control") != "no-store" || !bytes.Contains(compact.Body.Bytes(), []byte("response.compact")) {
		t.Fatalf("compact failed: %d %s", compact.Code, compact.Body.String())
	}
	control := request("GET", "/backend-api/codex/agent-identities/jwks?version=2", nil, "", true)
	if control.Code != http.StatusOK || len(provider.targets) != 1 || provider.targets[0].Account.ID != "acct" {
		t.Fatalf("control failed: %d %+v", control.Code, provider.targets)
	}
}

type codexTestAdmission struct{}

func (codexTestAdmission) Acquire(context.Context) (func(), error) { return func() {}, nil }

func TestCodexOperationsTranscriptionMultipartBoundary(t *testing.T) {
	secret := "synthetic-proxy-key"
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(secret)))
	key := domain.APIKey{ID: "key", KeyHash: hash, IsActive: true, CreatedAt: time.Now()}
	account := domain.Account{ID: "acct", Kind: domain.AccountChatGPT, Provider: "openai", Status: domain.AccountActive, CreatedAt: time.Now()}
	store := &operationsKeyStore{keys: map[string]domain.APIKey{"key": key}, accounts: []domain.Account{account}}
	provider := &operationsProvider{}
	service := application.NewCodexOperations(store, &codexOwnerStore{}, provider, time.Hour)
	service.ConfigureAdmission(codexTestAdmission{})
	service.ConfigureAccountSelection(application.NewProxy(store, nil, nil, application.ProxyConfig{}))
	handler := NewCodexOperationsHandler(store, service)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body, contentType := transcriptionTestForm()
	r := httptest.NewRequest("POST", "/v1/audio/transcriptions", body)
	r.Header.Set("Content-Type", contentType)
	r.Header.Set("Authorization", "Bearer "+secret)
	r.Header.Set("Content-Encoding", "br")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("compressed multipart was accepted: %d", w.Code)
	}
	body, contentType = transcriptionTestForm()
	r = httptest.NewRequest("POST", "/v1/audio/transcriptions", body)
	r.Header.Set("Content-Type", contentType)
	r.Header.Set("Authorization", "Bearer "+secret)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK || len(provider.targets) != 1 {
		t.Fatalf("transcription failed: %d %+v", w.Code, provider.targets)
	}
}

type codexOwnerStore struct{}

func (codexOwnerStore) SaveCodexResourceOwner(context.Context, application.CodexResourceOwner) error {
	return nil
}
func (codexOwnerStore) GetCodexResourceOwner(context.Context, string, string, string, time.Time) (application.CodexResourceOwner, error) {
	return application.CodexResourceOwner{}, domain.ErrNotFound
}

func transcriptionTestForm() (*bytes.Buffer, string) {
	body := &bytes.Buffer{}
	form := multipart.NewWriter(body)
	file, _ := form.CreateFormFile("file", "audio.wav")
	_, _ = file.Write([]byte("audio"))
	_ = form.WriteField("model", "gpt-4o-transcribe")
	_ = form.WriteField("response_format", "verbose_json")
	_ = form.Close()
	return body, form.FormDataContentType()
}
