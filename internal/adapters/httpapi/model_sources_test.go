package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

type testCipher struct{}

func (testCipher) Encrypt(value []byte) ([]byte, error) { return append([]byte("enc:"), value...), nil }
func (testCipher) Decrypt(value []byte) ([]byte, error) {
	if !strings.HasPrefix(string(value), "enc:") {
		return nil, errTestCipher
	}
	return value[len("enc:"):], nil
}

var errTestCipher = errors.New("invalid ciphertext")

type modelSourceMemory struct {
	items       map[string]domain.ModelSource
	credentials map[string]domain.AccountCredential
	nextID      int64
}

func newModelSourceMemory() *modelSourceMemory {
	return &modelSourceMemory{items: map[string]domain.ModelSource{}, credentials: map[string]domain.AccountCredential{}}
}

func (m *modelSourceMemory) ListModelSources(context.Context) ([]domain.ModelSource, error) {
	result := make([]domain.ModelSource, 0, len(m.items))
	for _, item := range m.items {
		result = append(result, item)
	}
	return result, nil
}
func (m *modelSourceMemory) GetModelSource(_ context.Context, id string) (domain.ModelSource, error) {
	item, ok := m.items[id]
	if !ok {
		return domain.ModelSource{}, domain.ErrNotFound
	}
	return item, nil
}
func (m *modelSourceMemory) SaveModelSource(_ context.Context, source domain.ModelSource, credential *domain.AccountCredential) error {
	source.Models = append([]domain.ModelSourceModel(nil), source.Models...)
	for i := range source.Models {
		if source.Models[i].ID == 0 {
			m.nextID++
			source.Models[i].ID = m.nextID
		}
	}
	m.items[source.ID] = source
	if credential != nil {
		m.credentials[source.ID] = *credential
	}
	return nil
}
func (m *modelSourceMemory) DeleteModelSource(_ context.Context, id string) error {
	if _, ok := m.items[id]; !ok {
		return domain.ErrNotFound
	}
	delete(m.items, id)
	delete(m.credentials, id)
	return nil
}

func TestModelSourceCRUDContract(t *testing.T) {
	memory := newModelSourceMemory()
	repository := struct {
		Repository
		*modelSourceMemory
	}{nil, memory}
	server := New(repository, testCipher{}, Config{}, nil)
	mux := http.NewServeMux()
	server.registerModelSourceRoutes(mux)

	request := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var payload []byte
		if body != nil {
			payload, _ = json.Marshal(body)
		}
		r := httptest.NewRequest(method, "http://localhost"+path, bytes.NewReader(payload))
		if body != nil {
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}

	createPayload := map[string]any{
		"name": "Custom Z.AI", "baseUrl": "http://127.0.0.1:1/v1", "apiKey": "source-key",
		"kind": "zai", "supportsChatCompletions": true, "timeoutSeconds": 30, "maxConcurrency": 2,
		"providerConfig": map[string]any{"allowNoSseDone": true, "extraBody": map[string]any{"safe": "on"}},
		"models": []any{map[string]any{
			"model": "public-model", "aliases": []string{"codex-alias"}, "upstreamModel": "glm-5.2",
			"displayName": "GLM", "contextWindow": 128000, "maxOutputTokens": 8192,
			"supportsStreaming": true, "supportsTools": true, "supportsVision": true,
			"inputPer1M": 2, "cachedInputPer1M": .2, "outputPer1M": 10,
			"rawMetadataJson": `{"supports_reasoning":true,"supported_reasoning_levels":["low","high"]}`,
			"isEnabled":       true,
		}},
	}
	created := request("POST", "/api/model-sources/", createPayload)
	var source domain.ModelSource
	if created.Code != http.StatusOK || json.Unmarshal(created.Body.Bytes(), &source) != nil ||
		source.ID == "" || len(source.Models) != 1 || source.Models[0].UpstreamModel != "glm-5.2" {
		t.Fatalf("create failed (%d): %s", created.Code, created.Body.String())
	}
	if credential := memory.credentials[source.ID]; len(credential.ExternalKeyEncrypted) == 0 ||
		bytes.Contains(created.Body.Bytes(), []byte("source-key")) {
		t.Fatal("credential was not encrypted or was disclosed")
	}

	list := request("GET", "/api/model-sources/", nil)
	var listPayload struct {
		Sources []domain.ModelSource `json:"sources"`
	}
	if list.Code != http.StatusOK || json.Unmarshal(list.Body.Bytes(), &listPayload) != nil || len(listPayload.Sources) != 1 {
		t.Fatalf("list failed (%d): %s", list.Code, list.Body.String())
	}

	update := request("PATCH", "/api/model-sources/"+source.ID, map[string]any{
		"name": "Renamed", "supportsChatCompletions": false, "supportsResponses": true, "models": []any{map[string]any{
			"model": "public-model", "aliases": []string{"codex-alias"}, "upstreamModel": "glm-5.3",
			"rawMetadataJson": `{"supports_reasoning":true}`,
		}},
	})
	if update.Code != http.StatusOK || json.Unmarshal(update.Body.Bytes(), &source) != nil ||
		source.Name != "Renamed" || source.Models[0].UpstreamModel != "glm-5.3" ||
		source.Kind != domain.ModelSourceZAI || source.Chat || !source.Responses || source.BaseURL != createPayload["baseUrl"] {
		t.Fatalf("update failed (%d): %s", update.Code, update.Body.String())
	}
	if bytes.Equal(memory.credentials[source.ID].ExternalKeyEncrypted, []byte("enc:source-key")) != true {
		t.Fatal("blank update discarded the stored credential")
	}
	if request("PATCH", "/api/model-sources/"+source.ID, map[string]any{"apiKey": nil}).Code != http.StatusOK ||
		len(memory.credentials[source.ID].ExternalKeyEncrypted) != 0 {
		t.Fatal("explicit null did not clear the credential")
	}
	if request("DELETE", "/api/model-sources/"+source.ID, nil).Code != http.StatusNoContent || len(memory.items) != 0 {
		t.Fatal("delete failed")
	}
}

func TestModelSourceValidationFailures(t *testing.T) {
	repository := struct {
		Repository
		*modelSourceMemory
	}{nil, newModelSourceMemory()}
	server := New(repository, testCipher{}, Config{}, nil)
	mux := http.NewServeMux()
	server.registerModelSourceRoutes(mux)
	for _, capabilities := range []string{
		`"supportsResponses":true,"supportsAudioTranscriptions":true`,
		`"supportsResponses":true,"supportsEmbeddings":true`,
		`"supportsResponses":false,"supportsChatCompletions":false`,
	} {
		r := httptest.NewRequest("POST", "/api/model-sources/", strings.NewReader(`{"name":"x","baseUrl":"http://x","apiKey":"k","kind":"zai",`+capabilities+`,"models":[{"model":"m"}]}`))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("invalid Z.AI capabilities %s status %d: %s", capabilities, w.Code, w.Body.String())
		}
	}
	r := httptest.NewRequest("POST", "/api/model-sources/", strings.NewReader(`{"name":"x","baseUrl":"http://x","future":true}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status %d: %s", w.Code, w.Body.String())
	}
}

func TestModelSourcePriceUsesCustomRates(t *testing.T) {
	price, err := application.ModelSourcePrice(domain.ModelSourceModel{
		InputPerMillion: floatPointer(2), OutputPerMillion: floatPointer(10), CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	cost, err := price.Cost(domain.UsageAmount{InputTokens: 1_000_000, OutputTokens: 1_000_000}, "")
	if err != nil || cost != 12_000_000 {
		t.Fatalf("custom price mismatch: cost=%d err=%v", cost, err)
	}
}

func floatPointer(value float64) *float64 { return &value }
