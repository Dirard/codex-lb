package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/adapters/upstream"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

// fakeAntigravityTokens lends versioned tokens and records refusals.
type fakeAntigravityTokens struct {
	calls     []int64
	failWith  error
	refusals  []string
	reportErr error
}

func (f *fakeAntigravityTokens) AntigravityAccessToken(_ context.Context, sourceID string, rejectedVersion int64) (string, int64, string, error) {
	f.calls = append(f.calls, rejectedVersion)
	if f.failWith != nil {
		return "", 0, "", f.failWith
	}
	version := int64(len(f.calls))
	return "google-access-" + string(rune('0'+version)), version, "aicode-consumers", nil
}

func (f *fakeAntigravityTokens) ReportAntigravityRefusal(_ context.Context, sourceID, code, actionURL string) error {
	f.refusals = append(f.refusals, sourceID+"|"+code+"|"+actionURL)
	return f.reportErr
}

func antigravitySource() (domain.ModelSource, domain.AccountCredential) {
	now := time.Now().UTC()
	zero := 0.0
	source := domain.ModelSource{
		ID: "src_antigravity", Name: "agent@example.test", Kind: domain.ModelSourceAntigravity, BaseURL: domain.AntigravityBaseURL,
		Enabled: true, Chat: true, Responses: true, CreatedAt: now, UpdatedAt: now,
		ProviderConfig: domain.ModelSourceProviderConfig{Antigravity: &domain.AntigravityAccountInfo{Email: "agent@example.test", Project: "aicode-consumers"}},
		Models: []domain.ModelSourceModel{
			{Model: "gemini-3.8-flash-high", Streaming: true, Tools: true, Vision: true, Enabled: true, InputPerMillion: &zero, OutputPerMillion: &zero,
				RawMetadataJSON: `{"supports_reasoning":true,"supported_reasoning_levels":["low","medium","high"],"default_reasoning_level":"high"}`},
			{Model: "gpt-oss-120b-medium", Streaming: true, Tools: true, Enabled: true, InputPerMillion: &zero, OutputPerMillion: &zero},
		},
	}
	// The credential row holds the refresh token; the provider never reads it.
	return source, domain.AccountCredential{AccountID: source.ID, ExternalKeyEncrypted: []byte("enc:google-refresh-token")}
}

type recordedCall struct {
	target upstream.Target
	stream bool
}

// antigravityVersion is the runtime setting the test adapters read on every
// call; a test may change it to show a saved version applies at once.
var antigravityVersion atomic.Value

func antigravityAdapter(t *testing.T, tokens *fakeAntigravityTokens, answers ...func() (application.ResponseResult, error)) (*Adapter, *[]recordedCall) {
	t.Helper()
	antigravityVersion.Store("2.35.1")
	source, credential := antigravitySource()
	adapter := New(&sourceStore{source: source, credential: credential}, tokenSource{}, testCipher{},
		Config{AntigravityTokens: tokens, AntigravityBaseURL: "https://cloudcode.example.test",
			ResolveAntigravityClientVersion: func(context.Context) (string, error) {
				version, _ := antigravityVersion.Load().(string)
				if version == "" {
					return "", errors.New("synthetic unreadable setting")
				}
				return version, nil
			}})
	t.Cleanup(func() { _ = adapter.Close() })
	calls := &[]recordedCall{}
	adapter.sourceCall = func(_ context.Context, target upstream.Target, _ json.RawMessage, stream bool,
		_ func(application.ResponseEvent) error, _ func()) (application.ResponseResult, error) {
		*calls = append(*calls, recordedCall{target: target, stream: stream})
		if len(*calls) > len(answers) {
			t.Fatalf("unexpected upstream call %d", len(*calls))
		}
		return answers[len(*calls)-1]()
	}
	return adapter, calls
}

func answered() (application.ResponseResult, error) {
	return application.ResponseResult{ResponseID: "resp_antigravity", UsageKnown: true}, nil
}

func refused(err *upstream.Error) func() (application.ResponseResult, error) {
	return func() (application.ResponseResult, error) {
		return application.ResponseResult{}, wrapUpstreamFailure(err)
	}
}

func antigravityTarget(source domain.ModelSource) application.ResponseTarget {
	account := source.Account()
	account.RequiresEgressDecision = false
	return application.ResponseTarget{Account: account, KeyID: "key", UseWebSocket: true}
}

func TestAntigravityDispatchBuildsTheCloudCodeTarget(t *testing.T) {
	tokens := &fakeAntigravityTokens{}
	adapter, calls := antigravityAdapter(t, tokens, answered, answered)
	source, _ := antigravitySource()
	body := `{"model":"gemini-3.8-flash-high","input":"hi","stream":true,"reasoning":{"effort":"medium"}}`
	if _, err := adapter.Respond(context.Background(), antigravityTarget(source), json.RawMessage(body), func(application.ResponseEvent) error { return nil }); err != nil {
		t.Fatal(err)
	}
	target := (*calls)[0].target
	wantAgent := "antigravity/hub/2.35.1 (aidev_client; os_type=" + runtime.GOOS + "; arch=" + runtime.GOARCH + ")"
	if target.BaseURL != "https://cloudcode.example.test" || target.Credential != "google-access-1" ||
		target.AntigravityProject != "aicode-consumers" || target.AntigravityUserAgent != wantAgent ||
		target.ProviderID != "antigravity" || target.AccountID != source.ID || target.KeyID != "key" {
		t.Fatalf("target: %+v", target)
	}
	capabilities := target.Capabilities
	if capabilities.Protocol != upstream.ProtocolAntigravity || capabilities.StreamTransport != upstream.TransportHTTP ||
		!capabilities.Tools || !capabilities.ParallelToolCalls || !capabilities.ImageInput || !capabilities.Reasoning ||
		len(capabilities.AllowedHostedTools) != 0 || capabilities.ModelMappings["gemini-3.8-flash-high"] != "gemini-3.8-flash-high" ||
		!(*calls)[0].stream {
		t.Fatalf("capabilities: %+v", capabilities)
	}
	// A model without image input or thinking says so in its capabilities.
	if _, err := adapter.Respond(context.Background(), antigravityTarget(source), json.RawMessage(`{"model":"gpt-oss-120b-medium","input":"hi"}`), nil); err != nil {
		t.Fatal(err)
	}
	if plain := (*calls)[1].target.Capabilities; plain.ImageInput || plain.Reasoning || !plain.Tools {
		t.Fatalf("plain model capabilities: %+v", plain)
	}
	if len(tokens.refusals) != 0 {
		t.Fatalf("a success reported a refusal: %v", tokens.refusals)
	}
}

func TestAntigravityDispatchRetriesOnceAfterARejectedToken(t *testing.T) {
	unauthorized := refused(&upstream.Error{Code: "invalid_api_key", Status: http.StatusUnauthorized})
	tokens := &fakeAntigravityTokens{}
	adapter, calls := antigravityAdapter(t, tokens, unauthorized, answered)
	source, _ := antigravitySource()
	result, err := adapter.Respond(context.Background(), antigravityTarget(source), json.RawMessage(`{"model":"gemini-3.8-flash-high","input":"hi"}`), nil)
	if err != nil || result.ResponseID != "resp_antigravity" || len(*calls) != 2 {
		t.Fatalf("retry: %+v %v after %d calls", result, err, len(*calls))
	}
	if (*calls)[1].target.Credential != "google-access-2" || len(tokens.calls) != 2 || tokens.calls[0] != -1 || tokens.calls[1] != 1 {
		t.Fatalf("the retry did not ask for a token newer than version 1: %v %q", tokens.calls, (*calls)[1].target.Credential)
	}

	tokens = &fakeAntigravityTokens{}
	adapter, calls = antigravityAdapter(t, tokens, unauthorized, unauthorized)
	_, err = adapter.Respond(context.Background(), antigravityTarget(source), json.RawMessage(`{"model":"gemini-3.8-flash-high","input":"hi"}`), nil)
	var failure *application.ProviderFailure
	if !errors.As(err, &failure) || failure.Status != http.StatusUnauthorized || len(*calls) != 2 {
		t.Fatalf("a second 401 must be returned without a third call: %v after %d calls", err, len(*calls))
	}
}

func TestAntigravityAccountRefusalsAreReported(t *testing.T) {
	source, _ := antigravitySource()
	cases := []struct {
		name   string
		err    *upstream.Error
		report string
	}{
		{"suspension", &upstream.Error{Code: upstream.ErrorCodeAccountSuspended, Status: 403, ActionURL: "https://forms.gle/appeal"},
			"src_antigravity|account_suspended|https://forms.gle/appeal"},
		{"verification", &upstream.Error{Code: upstream.ErrorCodeAccountValidationRequired, Status: 403, ActionURL: "https://accounts.example.test/verify"},
			"src_antigravity|account_validation_required|https://accounts.example.test/verify"},
		{"model access", &upstream.Error{Code: upstream.ErrorCodeModelAccessDenied, Status: 403}, ""},
		{"content", &upstream.Error{Code: upstream.ErrorCodeContentRefused, Status: 429}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tokens := &fakeAntigravityTokens{reportErr: errors.New("write failed")}
			adapter, calls := antigravityAdapter(t, tokens, refused(tc.err))
			_, err := adapter.Respond(context.Background(), antigravityTarget(source), json.RawMessage(`{"model":"gemini-3.8-flash-high","input":"hi"}`), nil)
			var failure *application.ProviderFailure
			if !errors.As(err, &failure) || failure.Code != tc.err.Code || failure.QuotaRefused || len(*calls) != 1 {
				t.Fatalf("refusal: %v after %d calls", err, len(*calls))
			}
			if tc.report == "" && len(tokens.refusals) != 0 || tc.report != "" && (len(tokens.refusals) != 1 || tokens.refusals[0] != tc.report) {
				t.Fatalf("reported: %v", tokens.refusals)
			}
		})
	}
}

func TestAntigravityUnavailableAccountFailsBeforeDispatch(t *testing.T) {
	tokens := &fakeAntigravityTokens{failWith: application.ErrAntigravityAccountUnavailable}
	adapter, calls := antigravityAdapter(t, tokens)
	source, _ := antigravitySource()
	_, err := adapter.Respond(context.Background(), antigravityTarget(source), json.RawMessage(`{"model":"gemini-3.8-flash-high","input":"hi"}`), nil)
	var failure *application.ProviderFailure
	if !errors.As(err, &failure) || failure.Code != "antigravity_account_unavailable" || failure.Dispatched || failure.QuotaRefused || len(*calls) != 0 {
		t.Fatalf("unavailable account: %v after %d calls", err, len(*calls))
	}
	withoutService := New(&sourceStore{source: source}, tokenSource{}, testCipher{}, Config{})
	defer withoutService.Close()
	if _, _, _, err := withoutService.antigravityToken(context.Background(), source, -1); !errors.As(err, &failure) || failure.Code != "antigravity_account_unavailable" {
		t.Fatalf("no token service: %v", err)
	}
}

func TestAntigravityRefusalsKeepTheirRetryHintAndStayOnTheAccount(t *testing.T) {
	cases := []struct {
		err   *upstream.Error
		quota bool
		retry string
	}{
		{&upstream.Error{Code: upstream.ErrorCodeInsufficientQuota, Status: 429, RetryAfter: time.Hour}, true, "3600"},
		{&upstream.Error{Code: upstream.ErrorCodeRateLimited, Status: 429, RetryAfter: 479 * time.Millisecond}, false, "1"},
		{&upstream.Error{Code: upstream.ErrorCodeCapacityExhausted, Status: 503, RetryAfter: 30 * time.Second}, false, "30"},
		{&upstream.Error{Code: upstream.ErrorCodeContentRefused, Status: 429}, false, ""},
		{&upstream.Error{Code: upstream.ErrorCodeModelAccessDenied, Status: 403}, false, ""},
		{&upstream.Error{Code: upstream.ErrorCodeAccountSuspended, Status: 403, ActionURL: "https://forms.gle/appeal"}, false, ""},
		{&upstream.Error{Code: upstream.ErrorCodeAccountValidationRequired, Status: 403}, false, ""},
		{&upstream.Error{Code: upstream.ErrorCodeRateLimited, Status: 429, RetryAfter: 72 * time.Hour}, false, "86400"},
	}
	for _, tc := range cases {
		var failure *application.ProviderFailure
		err := wrapUpstreamFailure(tc.err)
		if !errors.As(err, &failure) || failure.Code != tc.err.Code || failure.Status != tc.err.Status || failure.QuotaRefused != tc.quota {
			t.Fatalf("%s: %+v", tc.err.Code, failure)
		}
		retry := failure.LimitHeaders["Retry-After"]
		if tc.retry == "" && len(retry) != 0 || tc.retry != "" && (len(retry) != 1 || retry[0] != tc.retry) {
			t.Fatalf("%s Retry-After: %v", tc.err.Code, retry)
		}
	}
	var refusal *accountRefusal
	if err := wrapUpstreamFailure(&upstream.Error{Code: upstream.ErrorCodeAccountSuspended, Status: 403, ActionURL: "https://forms.gle/appeal"}); !errors.As(err, &refusal) ||
		refusal.actionURL != "https://forms.gle/appeal" || err.Error() != upstream.ErrorCodeAccountSuspended {
		t.Fatalf("the appeal link was lost or leaked into the message: %v", err)
	}
	// An image-limit refusal keeps its own reset headers.
	image := wrapUpstreamFailure(&upstream.Error{Code: upstream.ErrorCodeImageGenerationLimit, Status: 429, RetryAfter: time.Second,
		LimitHeaders: map[string][]string{"Retry-After": {"42"}}})
	var failure *application.ProviderFailure
	if !errors.As(image, &failure) || failure.LimitHeaders["Retry-After"][0] != "42" {
		t.Fatalf("image limit headers: %+v", failure)
	}
}

func TestAntigravityCredentialIsNeverAPassThroughBearer(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	source, credential := antigravitySource()
	source.BaseURL = server.URL
	adapter := New(&sourceStore{source: source, credential: credential}, tokenSource{}, testCipher{}, Config{HTTPClient: server.Client()})
	defer adapter.Close()
	_, err := adapter.NativeChat(context.Background(), antigravityTarget(source),
		json.RawMessage(`{"model":"gemini-3.8-flash-high","messages":[{"role":"user","content":"hi"}]}`), false, nil)
	var failure *application.ProviderFailure
	if !errors.As(err, &failure) || failure.Code != "model_source_protocol_unsupported" || calls.Load() != 0 {
		t.Fatalf("native chat on an Antigravity source: %v after %d calls", err, calls.Load())
	}
}

func TestTranslatedStreamsStartWithASyntheticCreatedEvent(t *testing.T) {
	for _, protocol := range []upstream.Protocol{upstream.ProtocolChatCompletions, upstream.ProtocolAntigravity} {
		if !syntheticStreamEvent(protocol, "response.created") || syntheticStreamEvent(protocol, "response.output_text.delta") {
			t.Fatalf("%s: the synthetic created event is not recognized", protocol)
		}
	}
	if syntheticStreamEvent(upstream.ProtocolResponses, "response.created") {
		t.Fatal("a Responses provider's own created event was treated as synthetic")
	}
}

func TestAntigravityClientVersionSettingAppliesToTheNextCall(t *testing.T) {
	tokens := &fakeAntigravityTokens{}
	adapter, calls := antigravityAdapter(t, tokens, answered, answered)
	source, _ := antigravitySource()
	body := json.RawMessage(`{"model":"gemini-3.8-flash-high","input":"hi"}`)
	if _, err := adapter.Respond(context.Background(), antigravityTarget(source), body, nil); err != nil {
		t.Fatal(err)
	}
	antigravityVersion.Store("2.36.0")
	if _, err := adapter.Respond(context.Background(), antigravityTarget(source), body, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix((*calls)[0].target.AntigravityUserAgent, "antigravity/hub/2.35.1 ") ||
		!strings.HasPrefix((*calls)[1].target.AntigravityUserAgent, "antigravity/hub/2.36.0 ") {
		t.Fatalf("User-Agents: %q, %q", (*calls)[0].target.AntigravityUserAgent, (*calls)[1].target.AntigravityUserAgent)
	}
	antigravityVersion.Store("")
	_, err := adapter.Respond(context.Background(), antigravityTarget(source), body, nil)
	var failure *application.ProviderFailure
	if !errors.As(err, &failure) || failure.Code != "antigravity_client_version_unavailable" || failure.Dispatched || len(*calls) != 2 {
		t.Fatalf("an unreadable setting was not refused before dispatch: %v, %d calls", err, len(*calls))
	}
}
