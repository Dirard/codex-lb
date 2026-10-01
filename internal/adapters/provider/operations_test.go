package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

func operationAdapter(t *testing.T, server *httptest.Server) (*Adapter, *sourceStore) {
	t.Helper()
	credential := domain.AccountCredential{AccountID: "acct", AccessTokenEncrypted: []byte("enc:token")}
	store := &sourceStore{credential: credential}
	adapter := New(store, tokenSource{}, testCipher{}, Config{HTTPClient: server.Client(), ChatGPTBaseURL: server.URL + "/codex", CodexVersion: "9.9.9"})
	return adapter, store
}

func operationAccount() domain.Account {
	return domain.Account{ID: "acct", Kind: domain.AccountChatGPT, Provider: "openai", ChatGPTAccountID: "chatgpt-account", Status: domain.AccountActive}
}

func TestOperationsCompactPreservesWireUsageAndHeaders(t *testing.T) {
	var request map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/codex/responses" || r.Header.Get("Accept") != "text/event-stream" || r.Header.Get("Authorization") != "Bearer chatgpt-access-token" ||
			r.Header.Get("ChatGPT-Account-ID") != "chatgpt-account" || r.Header.Get("Originator") != "codex_cli_rs" || r.Header.Get("Cookie") != "" {
			t.Fatalf("compact endpoint/headers invalid: %s %+v", r.URL.Path, r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &request)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: " + `{"type":"response.completed","response":{"object":"response","output":[{"type":"compaction","encrypted_content":"opaque"}],"service_tier":"default","usage":{"input_tokens":4,"output_tokens":2,"input_tokens_details":{"cached_tokens":1},"output_tokens_details":{"reasoning_tokens":2}}}}` + "\n\n"))
	}))
	defer server.Close()
	adapter, _ := operationAdapter(t, server)
	result, err := adapter.Compact(context.Background(), application.CodexOperationTarget{Account: operationAccount(), KeyID: "key", SessionID: "session", TurnState: "turn"}, mustJSON(map[string]any{"model": "gpt-6-sol", "input": "x", "store": true, "max_output_tokens": 16}))
	if err != nil {
		t.Fatal(err)
	}
	if request["max_output_tokens"] != nil || string(request["store"]) != "false" || string(request["stream"]) != "true" || string(request["input"]) != `[{"role":"user","content":"x"},{"type":"compaction_trigger"}]` || result.Usage.CachedInputTokens != 1 || result.Usage.ReasoningTokens != 2 || result.ServiceTier != "default" {
		t.Fatalf("compact result mismatch: %s %+v", request, result)
	}
}

func TestCompactHTTPFailureKeepsOnlyValidReportedBilling(t *testing.T) {
	for _, test := range []struct {
		name, usage string
		known       bool
	}{
		{"valid cached failure", `{"input_tokens":5,"prompt_tokens":5,"output_tokens":2,"completion_tokens":2,"total_tokens":7,"prompt_cache_hit_tokens":2,"prompt_cache_miss_tokens":3}`, true},
		{"conflicting failure", `{"input_tokens":0,"prompt_tokens":5,"output_tokens":2,"total_tokens":7}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(503)
				_, _ = w.Write([]byte(`{"error":{"code":"synthetic_failure"},"usage":` + test.usage + `,"service_tier":"priority"}`))
			}))
			defer server.Close()
			adapter, _ := operationAdapter(t, server)
			result, err := adapter.Compact(context.Background(), application.CodexOperationTarget{Account: operationAccount(), KeyID: "key"}, []byte(`{"model":"gpt-6-sol","input":"x"}`))
			var failure *application.ProviderFailure
			if result.UsageKnown != test.known || result.ServiceTier != "priority" || result.Failed != true ||
				test.known && (err != nil || result.Usage.InputTokens != 5 || result.Usage.CachedInputTokens != 2) ||
				!test.known && (!errors.As(err, &failure) || failure.Code != "invalid_upstream_usage") {
				t.Fatalf("compact failure billing: result=%+v error=%v", result, err)
			}
		})
	}
}

func TestOperationsControlAndFileContracts(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.RequestURI())
		switch {
		case r.URL.Path == "/codex/thread/goal/get":
			if r.Method != http.MethodGet || r.URL.Query().Get("version") != "2" {
				t.Fatalf("control request invalid: %s", paths[len(paths)-1])
			}
			_, _ = w.Write([]byte(`{"goal":"ok"}`))
		case r.URL.Path == "/files":
			_, _ = w.Write([]byte(`{"file_id":"file-1"}`))
		case r.URL.Path == "/files/file-1/uploaded":
			if string(readAll(t, r.Body)) != "{}" {
				t.Fatal("file finalize body changed")
			}
			if len(paths) == 3 {
				_, _ = w.Write([]byte(`{"status":"retry"}`))
				return
			}
			_, _ = w.Write([]byte(`{"status":"success"}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	adapter, _ := operationAdapter(t, server)
	target := application.CodexOperationTarget{Account: operationAccount(), KeyID: "key"}
	if result, err := adapter.Control(context.Background(), target, application.CodexControlRequest{
		Method: http.MethodGet, Path: "thread/goal/get", Query: [][2]string{{"version", "2"}},
	}); err != nil || result.Status != 200 {
		t.Fatalf("control failed: %+v %v", result, err)
	}
	if result, err := adapter.CreateFile(context.Background(), target, mustJSON(map[string]any{"file_name": "a.png", "file_size": 2})); err != nil || result.Status != 200 {
		t.Fatalf("file create failed: %+v %v", result, err)
	}
	if result, err := adapter.FinalizeFile(context.Background(), target, "file-1"); err != nil || result.Status != 200 || !strings.Contains(string(result.Body), "success") {
		t.Fatalf("file finalize failed: %+v %v", result, err)
	}
	if len(paths) != 4 {
		t.Fatalf("expected retry then success, got %v", paths)
	}
}

func TestOperationsRetriesExactlyOnceAfterPreOutput401(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"invalid_token"}}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer chatgpt-refreshed-token" {
			t.Fatalf("refreshed token not used: %+v", r.Header)
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	adapter, _ := operationAdapter(t, server)
	result, err := adapter.Control(context.Background(), application.CodexOperationTarget{Account: operationAccount(), KeyID: "key"}, application.CodexControlRequest{
		Method: http.MethodGet, Path: "thread/goal/get",
	})
	if err != nil || result.Status != 200 || calls != 2 {
		t.Fatalf("401 retry mismatch: calls=%d result=%+v err=%v", calls, result, err)
	}
}

func TestOperationsTranscriptionChatGPTAndExternal(t *testing.T) {
	chatServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/transcribe" || r.Header.Get("Authorization") != "Bearer chatgpt-access-token" {
			t.Fatalf("ChatGPT transcription request invalid: %s %+v", r.URL.Path, r.Header)
		}
		_, _ = w.Write([]byte(`{"text":"hello","duration":3}`))
	}))
	defer chatServer.Close()
	adapter, store := operationAdapter(t, chatServer)
	target := application.CodexOperationTarget{Account: operationAccount(), KeyID: "key"}
	result, err := adapter.Transcribe(context.Background(), target, application.CodexTranscriptionRequest{Audio: []byte("audio"), Filename: "a.wav", ContentType: "audio/wav"})
	if err != nil || result.AudioSeconds != 3 {
		t.Fatalf("ChatGPT transcription failed: %+v %v", result, err)
	}

	externalServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" || r.Header.Get("Authorization") != "Bearer external-key" {
			t.Fatalf("external transcription request invalid: %s %+v", r.URL.Path, r.Header)
		}
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			t.Fatal(err)
		}
		if r.FormValue("model") != "whisper-x" || r.FormValue("response_format") != "verbose_json" || r.ContentLength == 0 {
			t.Fatalf("external transcription form invalid: %+v", r.Form)
		}
		_, _ = w.Write([]byte(`{"text":"external","duration":5,"usage":{"prompt_tokens":2,"completion_tokens":1}}`))
	}))
	defer externalServer.Close()
	store.source = domain.ModelSource{
		ID: "src-audio", Kind: domain.ModelSourceOpenAICompatible, BaseURL: externalServer.URL + "/v1",
		Enabled: true, Audio: true, Models: []domain.ModelSourceModel{{Model: "whisper-x", Enabled: true}},
	}
	store.credential = domain.AccountCredential{AccountID: "src-audio", ExternalKeyEncrypted: []byte("enc:external-key")}
	externalTarget := application.CodexOperationTarget{Account: domain.Account{ID: "src-audio", Kind: domain.AccountExternal}, KeyID: "key"}
	result, err = adapter.Transcribe(context.Background(), externalTarget, application.CodexTranscriptionRequest{
		Model: "whisper-x", Audio: []byte("audio"), Filename: "a.wav", Fields: [][2]string{{"response_format", "verbose_json"}},
	})
	if err != nil || result.AudioSeconds != 5 || result.Usage.InputTokens != 2 || result.Usage.OutputTokens != 1 {
		t.Fatalf("external transcription failed: %+v %v", result, err)
	}
}

func readAll(t *testing.T, reader io.Reader) []byte {
	t.Helper()
	value, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
