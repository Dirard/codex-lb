package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

func TestExternalSourceEditDoesNotRedirectAdmittedTarget(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"response","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":0,"total_tokens":1}}`))
	}))
	defer server.Close()
	source := domain.ModelSource{ID: "source", Name: "Source", Kind: domain.ModelSourceOpenAICompatible,
		BaseURL: "https://old.example.invalid/v1", Enabled: true, Chat: true, Responses: true,
		Embeddings: true, Audio: true, Models: []domain.ModelSourceModel{{Model: "model", Enabled: true}}}
	oldAccount := source.Account()
	source.BaseURL = server.URL + "/v1"
	store := &sourceStore{source: source, credential: domain.AccountCredential{AccountID: source.ID,
		RouteRevision: 1, ExternalKeyEncrypted: []byte("enc:test-key")}}
	adapter := New(store, tokenSource{}, testCipher{}, Config{HTTPClient: server.Client()})
	ctx := context.Background()
	target := application.ResponseTarget{Account: oldAccount, KeyID: "key"}
	for _, check := range []struct {
		name string
		call func() error
	}{
		{"responses", func() error {
			_, err := adapter.Respond(ctx, target, []byte(`{"model":"model","input":"hi"}`), nil)
			return err
		}},
		{"native chat", func() error {
			_, err := adapter.NativeChat(ctx, target, []byte(`{"model":"model","messages":[{"role":"user","content":"hi"}]}`), false, nil)
			return err
		}},
		{"embeddings", func() error {
			_, err := adapter.Embeddings(ctx, target, []byte(`{"model":"model","input":"hi"}`))
			return err
		}},
		{"transcription", func() error {
			_, err := adapter.Transcribe(ctx, application.CodexOperationTarget{Account: oldAccount, KeyID: "key"},
				application.CodexTranscriptionRequest{Model: "model", Audio: []byte("audio")})
			return err
		}},
	} {
		t.Run(check.name, func(t *testing.T) {
			before := calls.Load()
			if err := check.call(); err == nil {
				t.Error("stale route was accepted")
			}
			if calls.Load() != before {
				t.Error("admitted old-route body and credential reached the new endpoint")
			}
		})
	}
}
