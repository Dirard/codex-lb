package httpapi_test

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/adapters/httpapi"
	"codex-lb/internal/adapters/provider"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

func TestTranscriptionAccountsOnlyConfirmedBillingUnits(t *testing.T) {
	for _, test := range []struct {
		name, response       string
		status               int
		subscription, minute bool
		uncertain            bool
		cost, input, output  int64
	}{
		{"subscription text only", `{"text":"ok"}`, 200, true, false, false, 0, 0, 0},
		{"external missing units", `{"text":"ok"}`, 200, false, false, true, 0, 0, 0},
		{"minute duration", `{"text":"ok","duration":5}`, 200, false, true, false, 5000, 0, 0},
		{"minute explicit zero", `{"text":"silence","duration":0}`, 200, false, true, false, 0, 0, 0},
		{"minute ignores wrong billing unit", `{"text":"ok","usage":{"input_tokens":2,"output_tokens":1}}`, 200, false, true, true, 0, 0, 0},
		{"negative duration", `{"text":"ok","duration":-1}`, 200, false, true, true, 0, 0, 0},
		{"excessive duration", `{"text":"ok","duration":121}`, 200, false, true, true, 0, 0, 0},
		{"token explicit zero", `{"text":"ok","usage":{"input_tokens":0,"output_tokens":0}}`, 200, false, false, false, 0, 0, 0},
		{"token counters", `{"text":"ok","usage":{"input_tokens":2,"output_tokens":1}}`, 200, false, false, false, 4, 2, 1},
		{"duration cannot replace tokens", `{"text":"ok","duration":5}`, 200, false, false, true, 0, 0, 0},
		{"charged minute refusal", `{"error":{"code":"invalid_request"},"duration":5}`, 400, false, true, false, 5000, 0, 0},
		{"definitive refusal", `{"error":{"code":"invalid_request"}}`, 400, false, false, false, 0, 0, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				fmt.Fprint(w, test.response)
			}))
			defer upstream.Close()
			_, store, proxy := wireFixtureWithProxy(t, nil)
			ctx := context.Background()
			vault, err := credentials.Open(filepath.Join(t.TempDir(), "synthetic-key"), true)
			if err != nil {
				t.Fatal(err)
			}
			model := "gpt-4o-transcribe"
			if !test.subscription {
				model = "audio-test"
				in, out, minute := 1.0, 2.0, 0.06
				entry := domain.ModelSourceModel{Model: model, Enabled: true, InputPerMillion: &in, OutputPerMillion: &out, CreatedAt: time.Now(), UpdatedAt: time.Now()}
				if test.minute {
					entry.AudioPerMinute = &minute
				}
				source := domain.ModelSource{ID: "audio-source", Name: "Synthetic audio", Kind: domain.ModelSourceOpenAICompatible, BaseURL: upstream.URL + "/v1", Enabled: true, Audio: true, Models: []domain.ModelSourceModel{entry}, CreatedAt: time.Now(), UpdatedAt: time.Now()}
				secret, _ := vault.Encrypt([]byte("synthetic-audio-credential"))
				if err := store.SaveModelSource(ctx, source, &domain.AccountCredential{AccountID: source.ID, ExternalKeyEncrypted: secret}); err != nil {
					t.Fatal(err)
				}
			}
			key, err := store.GetAPIKey(ctx, "wire-key")
			if err != nil {
				t.Fatal(err)
			}
			key.Limits[0].MaxValue = 1
			if err := store.SaveAPIKey(ctx, key, time.Now()); err != nil {
				t.Fatal(err)
			}
			adapter := provider.New(store, fixedTokenSource{}, vault, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL + "/codex"})
			defer adapter.Close()
			operations := application.NewCodexOperations(store, store, adapter, time.Hour)
			operations.ConfigureAdmission(proxy)
			mux := http.NewServeMux()
			httpapi.RegisterCodexOperationRoutes(mux, store, operations)
			post := func() *httptest.ResponseRecorder {
				var body bytes.Buffer
				form := multipart.NewWriter(&body)
				_ = form.WriteField("model", model)
				file, _ := form.CreateFormFile("file", "synthetic.wav")
				_, _ = file.Write([]byte("synthetic audio"))
				_ = form.Close()
				path := "/v1/audio/transcriptions/"
				if test.subscription {
					path = "/backend-api/transcribe/"
				}
				r := httptest.NewRequest("POST", path, &body)
				r.Header.Set("Content-Type", form.FormDataContentType())
				r.Header.Set("Authorization", "Bearer synthetic-key")
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, r)
				return w
			}
			response := post()
			pending, err := store.ListReservationsNeedingReconciliation(ctx, "", 10)
			if err != nil || (len(pending) == 1) != test.uncertain || calls.Load() != 1 {
				t.Fatalf("billing certainty mismatch: status=%d pending=%+v calls=%d err=%v body=%s", response.Code, pending, calls.Load(), err, response.Body.String())
			}
			totals, err := store.UsageTotals(ctx, key.ID, "")
			if err != nil {
				t.Fatal(err)
			}
			if test.uncertain {
				if response.Code != 502 || totals.RequestCount != 0 || totals.Usage != (domain.UsageAmount{}) {
					t.Fatalf("unknown billing emitted success/zero event: %d %+v", response.Code, totals)
				}
				if next := post(); next.Code != 429 || calls.Load() != 1 {
					t.Fatal("uncertain audio budget was bypassed")
				}
			} else if response.Code != test.status || totals.RequestCount != 1 || totals.Usage.CostMicrodollars != test.cost || totals.Usage.InputTokens != test.input || totals.Usage.OutputTokens != test.output {
				t.Fatalf("confirmed billing not settled once: status=%d %+v body=%s", response.Code, totals, response.Body.String())
			}
		})
	}
}
