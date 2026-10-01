package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

type compactV2Provider struct {
	operationsProvider
	body []byte
}

func (p *compactV2Provider) Compact(context.Context, application.CodexOperationTarget, json.RawMessage) (application.CodexOperationResult, error) {
	return application.CodexOperationResult{Status: 200, UsageKnown: true, Body: p.body}, nil
}

func TestCodexStandaloneCompactNormalizesV2WithoutChangingV1(t *testing.T) {
	const secret = "synthetic-compaction-key"
	key := domain.APIKey{ID: "key", KeyHash: fmt.Sprintf("%x", sha256.Sum256([]byte(secret))), IsActive: true}
	store := &operationsKeyStore{keys: map[string]domain.APIKey{"key": key}, accounts: []domain.Account{{ID: "account", Kind: domain.AccountChatGPT, Status: domain.AccountActive}}}
	for _, payload := range []string{
		`{"object":"response.compact","output":[{"type":"message","content":"past input"},{"type":"compaction_summary","id":"cmp_opaque_id","status":"completed","encrypted_content":"opaque"}],"counter":9007199254740993}`,
		`{"object":"response.compact","output":[{"type":"message","content":"past input"}],"compaction_summary":{"id":"cmp_opaque_id","status":"completed","encrypted_content":"opaque"},"counter":9007199254740993}`,
	} {
		provider := &compactV2Provider{body: []byte(payload)}
		service := application.NewCodexOperations(store, &codexOwnerStore{}, provider, time.Hour)
		service.ConfigureAdmission(codexTestAdmission{})
		service.ConfigureAccountSelection(application.NewProxy(store, nil, nil, application.ProxyConfig{}))
		handler := NewCodexOperationsHandler(store, service)
		for _, route := range []string{"/backend-api/codex/responses/compact", "/backend-api/codex/responses/compact/", "/v1/responses/compact", "/v1/responses/compact/"} {
			request := httptest.NewRequest("POST", route, strings.NewReader(`{"model":"gpt-6-sol","instructions":"","input":"history"}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer "+secret)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != 200 {
				t.Fatalf("%s failed: %d", route, response.Code)
			}
			if strings.HasPrefix(route, "/v1/") {
				if response.Body.String() != payload {
					t.Fatal("OpenAI-style compact payload was changed")
				}
				continue
			}
			var result struct {
				Output []map[string]string `json:"output"`
			}
			if json.Unmarshal(response.Body.Bytes(), &result) != nil || len(result.Output) != 1 || result.Output[0]["type"] != "compaction" || result.Output[0]["id"] != "cmp_opaque_id" || result.Output[0]["status"] != "completed" || result.Output[0]["encrypted_content"] != "opaque" {
				t.Fatalf("compaction identity was not preserved: %s", response.Body.String())
			}
			if !bytes.Contains(response.Body.Bytes(), []byte("9007199254740993")) {
				t.Fatal("unknown numeric payload was rounded")
			}
		}
	}
}
