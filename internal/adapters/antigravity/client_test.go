package antigravity

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"codex-lb/internal/application"
)

// googleStub is a loopback stand-in for Google's token endpoint, userinfo and
// Cloud Code control methods. It records each request's headers and form.
type googleStub struct {
	t        *testing.T
	server   *httptest.Server
	mu       sync.Mutex
	forms    []url.Values
	headers  map[string]http.Header
	bodies   map[string][]byte
	onboards int
}

func newGoogleStub(t *testing.T) *googleStub {
	stub := &googleStub{t: t, headers: map[string]http.Header{}, bodies: map[string][]byte{}}
	stub.server = httptest.NewServer(http.HandlerFunc(stub.serve))
	t.Cleanup(stub.server.Close)
	return stub
}

func (g *googleStub) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	g.mu.Lock()
	g.headers[r.URL.Path] = r.Header.Clone()
	g.bodies[r.URL.Path] = body
	g.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/token":
		form, _ := url.ParseQuery(string(body))
		g.mu.Lock()
		g.forms = append(g.forms, form)
		g.mu.Unlock()
		if form.Get("code") == "refused-code" || form.Get("refresh_token") == "revoked-refresh" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Bad Request revoked-refresh"}`))
			return
		}
		if form.Get("refresh_token") == "broken-refresh" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"internal_failure"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"synthetic-access","expires_in":3599,"refresh_token":"synthetic-refresh","token_type":"Bearer"}`))
	case "/userinfo":
		if r.Header.Get("Authorization") != "Bearer synthetic-access" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"email":"agent@example.test","verified_email":true}`))
	case "/v1internal:loadCodeAssist":
		_, _ = w.Write([]byte(`{"currentTier":{"id":"free-tier"},"paidTier":{"id":"g1-pro-tier"},"allowedTiers":[{"id":"legacy-tier"},{"id":"standard-tier","isDefault":true}],"cloudaicompanionProject":{"id":"aicode-consumers"}}`))
	case "/v1internal:onboardUser":
		g.mu.Lock()
		g.onboards++
		done := g.onboards >= 2
		g.mu.Unlock()
		if !done {
			_, _ = w.Write([]byte(`{"name":"operations/onboard-1","done":false}`))
			return
		}
		_, _ = w.Write([]byte(`{"name":"operations/onboard-1","done":true,"response":{"cloudaicompanionProject":{"id":"onboarded-project"}}}`))
	case "/v1internal:fetchAvailableModels":
		if r.Header.Get("Authorization") == "Bearer stale-access" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"models":{
"gemini-3.8-flash-high":{"displayName":"Gemini 3.8 Flash","maxTokens":1048576,"maxOutputTokens":"65536","supportsImages":true,"supportsThinking":true,"quotaInfo":{"remainingFraction":0.85}},
"claude-opus-5-5-high":{"displayName":"Claude Opus 5.5","maxTokens":1000000,"maxOutputTokens":128000,"supportsImages":true,"supportsThinking":true},
"gpt-oss-120b-medium":{"displayName":"GPT-OSS 120B","maxTokens":114000,"maxOutputTokens":32768,"disabled":true},
"tab_flash_lite_preview":{"displayName":"Tab"},"chat_20706":{},"gemini-pro-agent":{},"gemini-pro-orchestrator-g1-pro":{},
"gemini-3.1-flash-image":{"displayName":"Image"},"nano-banana-pro":{"displayName":"Image generator"}},
"imageGenerationModelIds":["nano-banana-pro"],"webSearchModelIds":["gemini-3.8-flash-high"]}`))
	default:
		g.t.Errorf("unexpected Google path %s", r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func (g *googleStub) client() *Client {
	return New(Config{HTTPClient: g.server.Client(), ResolveClientVersion: func(context.Context) (string, error) { return "2.35.1", nil }, TokenURL: g.server.URL + "/token",
		UserInfoURL: g.server.URL + "/userinfo", CloudCodeBaseURL: g.server.URL, OnboardInterval: time.Millisecond})
}

func TestAuthorizationURLAsksForOfflinePKCEConsent(t *testing.T) {
	raw := New(Config{}).AuthorizationURL("synthetic-state", "synthetic-challenge")
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme+"://"+parsed.Host+parsed.Path != DefaultAuthURL {
		t.Fatalf("authorization URL %q: %v", raw, err)
	}
	query := parsed.Query()
	want := map[string]string{
		"client_id": ClientID, "redirect_uri": "https://antigravity.google/oauth-callback", "response_type": "code",
		"access_type": "offline", "prompt": "consent", "state": "synthetic-state",
		"code_challenge": "synthetic-challenge", "code_challenge_method": "S256",
		"scope": "https://www.googleapis.com/auth/cloud-platform https://www.googleapis.com/auth/userinfo.email " +
			"https://www.googleapis.com/auth/userinfo.profile https://www.googleapis.com/auth/cclog " +
			"https://www.googleapis.com/auth/experimentsandconfigs https://www.googleapis.com/auth/aicode",
	}
	for name, value := range want {
		if query.Get(name) != value {
			t.Errorf("%s = %q, want %q", name, query.Get(name), value)
		}
	}
	if query.Has("client_secret") {
		t.Fatal("the authorization URL carries the client secret")
	}
}

func TestTokenExchangeAndRefreshUseTheInstalledAppClient(t *testing.T) {
	stub := newGoogleStub(t)
	client := stub.client()
	ctx := context.Background()
	tokens, err := client.ExchangeCode(ctx, "4/synthetic-code", "synthetic-verifier")
	if err != nil || tokens.Access != "synthetic-access" || tokens.Refresh != "synthetic-refresh" || tokens.ExpiresIn != 3599*time.Second {
		t.Fatalf("exchange: %+v %v", tokens, err)
	}
	exchange := stub.forms[0]
	if exchange.Get("grant_type") != "authorization_code" || exchange.Get("code") != "4/synthetic-code" ||
		exchange.Get("code_verifier") != "synthetic-verifier" || exchange.Get("redirect_uri") != RedirectURI ||
		exchange.Get("client_id") != ClientID || exchange.Get("client_secret") != clientSecret {
		t.Fatalf("exchange form: %v", exchange)
	}
	if _, err := client.Refresh(ctx, "stored-refresh"); err != nil {
		t.Fatal(err)
	}
	refresh := stub.forms[1]
	if refresh.Get("grant_type") != "refresh_token" || refresh.Get("refresh_token") != "stored-refresh" || refresh.Get("client_secret") != clientSecret {
		t.Fatalf("refresh form: %v", refresh)
	}
	if _, err := client.Refresh(ctx, "revoked-refresh"); !errors.Is(err, application.ErrAntigravityGrantRefused) {
		t.Fatalf("invalid_grant refresh: %v", err)
	}
	if _, err := client.ExchangeCode(ctx, "refused-code", "v"); !errors.Is(err, application.ErrAntigravityGrantRefused) {
		t.Fatalf("invalid_grant exchange: %v", err)
	}
	_, err = client.Refresh(ctx, "broken-refresh")
	if err == nil || errors.Is(err, application.ErrAntigravityGrantRefused) || strings.Contains(err.Error(), "broken-refresh") {
		t.Fatalf("a server failure must be transient and quote nothing: %v", err)
	}
}

func TestCloudCodeCallsSendOnlyTheAgreedHeaders(t *testing.T) {
	stub := newGoogleStub(t)
	client := stub.client()
	ctx := context.Background()
	email, err := client.UserEmail(ctx, "synthetic-access")
	if err != nil || email != "agent@example.test" {
		t.Fatalf("userinfo: %q %v", email, err)
	}
	if _, err := client.UserEmail(ctx, "other-access"); !errors.Is(err, application.ErrAntigravityUnauthorized) {
		t.Fatalf("rejected userinfo token: %v", err)
	}
	assist, err := client.LoadCodeAssist(ctx, "synthetic-access")
	if err != nil || assist.Project != "aicode-consumers" || assist.Tier != "g1-pro-tier" || assist.OnboardTier != "standard-tier" {
		t.Fatalf("loadCodeAssist: %+v %v", assist, err)
	}
	if body := string(stub.bodies["/v1internal:loadCodeAssist"]); body != `{"metadata":{"ideType":"ANTIGRAVITY"}}` {
		t.Fatalf("loadCodeAssist body: %s", body)
	}
	wantAgent := "antigravity/hub/2.35.1 (aidev_client; os_type=" + runtime.GOOS + "; arch=" + runtime.GOARCH + ")"
	for _, path := range []string{"/v1internal:loadCodeAssist", "/userinfo", "/token"} {
		if path == "/token" {
			if _, err := client.Refresh(ctx, "stored-refresh"); err != nil {
				t.Fatal(err)
			}
		}
		headers := stub.headers[path]
		if headers.Get("User-Agent") != wantAgent {
			t.Errorf("%s User-Agent = %q", path, headers.Get("User-Agent"))
		}
		for name := range headers {
			switch name {
			case "Content-Type", "Authorization", "User-Agent", "Accept-Encoding", "Content-Length":
			default:
				t.Errorf("%s sent the extra header %s", path, name)
			}
		}
	}
	if !strings.HasPrefix(stub.headers["/v1internal:loadCodeAssist"].Get("Authorization"), "Bearer ") ||
		stub.headers["/v1internal:loadCodeAssist"].Get("Content-Type") != "application/json" {
		t.Fatalf("loadCodeAssist headers: %v", stub.headers["/v1internal:loadCodeAssist"])
	}
}

func TestOnboardingPollsTheOperationUntilDone(t *testing.T) {
	stub := newGoogleStub(t)
	project, err := stub.client().OnboardUser(context.Background(), "synthetic-access", "standard-tier")
	if err != nil || project != "onboarded-project" || stub.onboards != 2 {
		t.Fatalf("onboarding: %q after %d calls: %v", project, stub.onboards, err)
	}
	var body struct {
		TierID   string            `json:"tierId"`
		Metadata map[string]string `json:"metadata"`
	}
	if json.Unmarshal(stub.bodies["/v1internal:onboardUser"], &body) != nil || body.TierID != "standard-tier" || body.Metadata["ideType"] != "ANTIGRAVITY" {
		t.Fatalf("onboardUser body: %s", stub.bodies["/v1internal:onboardUser"])
	}
}

func TestOnboardingGivesUpAfterBoundedPolls(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"name":"operations/slow","done":false}`))
	}))
	defer server.Close()
	client := New(Config{HTTPClient: server.Client(), CloudCodeBaseURL: server.URL, OnboardInterval: time.Millisecond})
	if _, err := client.OnboardUser(context.Background(), "synthetic-access", "free-tier"); err == nil || calls != onboardAttempts {
		t.Fatalf("unfinished onboarding: %v after %d calls", err, calls)
	}
}

func TestAvailableModelsLeavesOutInternalAndImageModels(t *testing.T) {
	stub := newGoogleStub(t)
	models, err := stub.client().AvailableModels(context.Background(), "synthetic-access", "aicode-consumers")
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	if strings.Join(ids, ",") != "claude-opus-5-5-high,gemini-3.8-flash-high,gpt-oss-120b-medium" {
		t.Fatalf("listed ids: %v", ids)
	}
	flash := models[1]
	if flash.DisplayName != "Gemini 3.8 Flash" || flash.ContextWindow != 1048576 || flash.MaxOutputTokens != 65536 || !flash.Images || !flash.Thinking || flash.Disabled {
		t.Fatalf("flash model: %+v", flash)
	}
	if !models[2].Disabled {
		t.Fatalf("a disabled model lost its flag: %+v", models[2])
	}
	if body := string(stub.bodies["/v1internal:fetchAvailableModels"]); body != `{"project":"aicode-consumers"}` {
		t.Fatalf("fetchAvailableModels body: %s", body)
	}
	if _, err := stub.client().AvailableModels(context.Background(), "stale-access", ""); !errors.Is(err, application.ErrAntigravityUnauthorized) {
		t.Fatalf("a rejected token must be reported: %v", err)
	}
}

func TestCloudCodeResponsesAreBounded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"models":{"x":{"displayName":"` + strings.Repeat("a", maxCloudCodeBytes) + `"}}}`))
	}))
	defer server.Close()
	client := New(Config{HTTPClient: server.Client(), CloudCodeBaseURL: server.URL})
	if _, err := client.AvailableModels(context.Background(), "synthetic-access", ""); err == nil || !strings.Contains(err.Error(), "bound") {
		t.Fatalf("an oversized answer was read: %v", err)
	}
}

func TestClientVersionValidation(t *testing.T) {
	for _, version := range []string{"2.34.0", "10.0.12345"} {
		if !ValidClientVersion(version) {
			t.Errorf("%q rejected", version)
		}
	}
	for _, version := range []string{"", "2.34", "2.34.0-beta", "v2.34.0", "2.34.0 (cl=1)", "2.34.0\n"} {
		if ValidClientVersion(version) {
			t.Errorf("%q accepted", version)
		}
	}
	if agent := UserAgent("bad version"); !strings.HasPrefix(agent, "antigravity/hub/"+DefaultClientVersion+" (aidev_client; os_type=") || strings.Contains(agent, "cl=") {
		t.Fatalf("fallback User-Agent: %q", agent)
	}
}
