package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"codex-lb/internal/adapters/antigravity"
	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/adapters/provider"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
	"codex-lb/internal/domain/pricing"
)

// antigravityCloud is one loopback stand-in for Google's OAuth endpoints and
// Cloud Code, including streamGenerateContent. mode picks the next answer.
type antigravityCloud struct {
	server *httptest.Server
	mu     sync.Mutex
	mode   string
	bodies []string
	agents []string
}

func newAntigravityCloud(t *testing.T) *antigravityCloud {
	c := &antigravityCloud{mode: "tool"}
	c.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		defer c.mu.Unlock()
		if r.URL.Path != "/token" && r.Header.Get("Authorization") != "Bearer synthetic-cloud-access" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			_, _ = w.Write([]byte(`{"access_token":"synthetic-cloud-access","expires_in":3599,"refresh_token":"synthetic-cloud-refresh","token_type":"Bearer"}`))
		case "/userinfo":
			_, _ = w.Write([]byte(`{"email":"agent@example.test"}`))
		case "/v1internal:loadCodeAssist":
			_, _ = w.Write([]byte(`{"cloudaicompanionProject":"aicode-consumers","paidTier":{"id":"g1-pro-tier"}}`))
		case "/v1internal:fetchAvailableModels":
			_, _ = w.Write([]byte(`{"models":{"gemini-3.8-flash-high":{"displayName":"Gemini 3.8 Flash","maxTokens":1048576,"maxOutputTokens":65536,"supportsImages":true,"supportsThinking":true}}}`))
		case "/v1internal:streamGenerateContent":
			c.bodies = append(c.bodies, string(body))
			c.agents = append(c.agents, r.Header.Get("User-Agent"))
			switch c.mode {
			case "tool":
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"Checking the files.","thought":true},{"thoughtSignature":"sig-1","functionCall":{"id":"call_ls","name":"shell","args":{"cmd":"ls"}}}]}}],"usageMetadata":{"promptTokenCount":40,"candidatesTokenCount":6,"thoughtsTokenCount":4,"totalTokenCount":50}},"traceId":"t1"}`+"\n\n")
			case "answer":
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"There is a.txt."}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":60,"candidatesTokenCount":5,"totalTokenCount":65}},"traceId":"t2"}`+"\n\n")
			case "quota":
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = io.WriteString(w, `{"error":{"code":429,"message":"quota","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED","domain":"cloudcode-pa.googleapis.com"},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"3600s"}]}}`)
			case "tos":
				w.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(w, `{"error":{"code":403,"message":"disabled","status":"PERMISSION_DENIED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"TOS_VIOLATION","domain":"cloudcode-pa.googleapis.com","metadata":{"appeal_url":"https://forms.gle/synthetic-appeal"}}]}}`)
			}
		default:
			t.Errorf("unexpected Cloud Code path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(c.server.Close)
	return c
}

func (c *antigravityCloud) set(mode string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mode = mode
}

func (c *antigravityCloud) requests() ([]string, []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.bodies...), append([]string(nil), c.agents...)
}

// antigravityRoute is a proxy whose only account for the model is a freshly
// signed-in Antigravity source talking to the loopback Cloud Code.
type antigravityRoute struct {
	cloud *antigravityCloud
	store interface {
		GetAccount(context.Context, string) (domain.Account, error)
		GetModelSource(context.Context, string) (domain.ModelSource, error)
		GetAPIKey(context.Context, string) (domain.APIKey, error)
	}
	source domain.ModelSource
	post   func(thread, body string) (int, string)
}

func newAntigravityRoute(t *testing.T) antigravityRoute {
	t.Helper()
	ctx := context.Background()
	cloud := newAntigravityCloud(t)
	var adapter *provider.Adapter
	server, store, proxy := wireFixtureWithProxy(t, wireProvider(func(ctx context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		return adapter.Respond(ctx, target, body, emit)
	}))
	proxy.ResolvePrice = func(context.Context, domain.Account, string) (pricing.Price, error) { return pricing.Price{}, nil }
	vault, err := credentials.Open(filepath.Join(t.TempDir(), "antigravity.key"), true)
	if err != nil {
		t.Fatal(err)
	}
	service := application.NewAntigravityService(store, vault, antigravity.New(antigravity.Config{HTTPClient: cloud.server.Client(),
		TokenURL: cloud.server.URL + "/token", UserInfoURL: cloud.server.URL + "/userinfo", CloudCodeBaseURL: cloud.server.URL, OnboardInterval: time.Millisecond}))
	adapter = provider.New(store, fixedTokenSource{}, nil, provider.Config{HTTPClient: cloud.server.Client(), AntigravityTokens: service, AntigravityBaseURL: cloud.server.URL})
	t.Cleanup(func() { adapter.Close() })

	start, err := service.StartSignIn(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	source, err := service.CompleteSignIn(ctx, start.FlowID, "4/synthetic-code")
	if err != nil {
		t.Fatal(err)
	}
	if !source.StickyOnly {
		t.Fatal("a new Antigravity source did not start sticky-only")
	}
	chatgpt, err := store.GetAccount(ctx, "wire-account")
	if err != nil {
		t.Fatal(err)
	}
	chatgpt.Status = domain.AccountPaused // only the Antigravity account can answer
	if err := store.SaveAccount(ctx, chatgpt); err != nil {
		t.Fatal(err)
	}

	post := func(thread, body string) (int, string) {
		t.Helper()
		request, _ := http.NewRequest(http.MethodPost, server.URL+"/backend-api/codex/responses", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer synthetic-key")
		request.Header.Set("Session_id", "antigravity-client")
		request.Header.Set("Thread-Id", thread)
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(response.Body)
		response.Body.Close()
		return response.StatusCode, string(data)
	}
	return antigravityRoute{cloud: cloud, store: store, source: source, post: post}
}

// A signed-in Antigravity account serves Codex turns end to end: a tool call
// whose thought signature comes back on the next turn, and a quota refusal
// kept on the sticky account.
func TestAntigravitySourceServesCodexEndToEnd(t *testing.T) {
	ctx := context.Background()
	route := newAntigravityRoute(t)
	cloud, store, post := route.cloud, route.store, route.post
	tools := `"tools":[{"type":"function","name":"shell","description":"Run a command","parameters":{"type":"object","properties":{"cmd":{"type":"string"}},"required":["cmd"],"additionalProperties":false}}]`

	status, body := post("thread", `{"model":"gemini-3.8-flash-high","stream":true,"instructions":"Be brief.",`+tools+`,"input":[{"role":"user","content":"list files"}]}`)
	if status != http.StatusOK || !strings.Contains(body, `"type":"function_call"`) || !strings.Contains(body, `"call_id":"call_ls"`) || !strings.Contains(body, "response.completed") {
		t.Fatalf("tool turn: %d %s", status, body)
	}

	cloud.set("answer")
	status, body = post("thread", `{"model":"gemini-3.8-flash-high","stream":true,"instructions":"Be brief.",`+tools+`,"input":[{"role":"user","content":"list files"},{"type":"function_call","call_id":"call_ls","name":"shell","arguments":"{\"cmd\":\"ls\"}"},{"type":"function_call_output","call_id":"call_ls","output":"a.txt"}]}`)
	if status != http.StatusOK || !strings.Contains(body, "There is a.txt.") {
		t.Fatalf("answer turn: %d %s", status, body)
	}
	bodies, agents := cloud.requests()
	if len(bodies) != 2 {
		t.Fatalf("Cloud Code calls: %d", len(bodies))
	}
	var replay struct {
		Project string `json:"project"`
		Request struct {
			Contents []struct {
				Role  string            `json:"role"`
				Parts []json.RawMessage `json:"parts"`
			} `json:"contents"`
		} `json:"request"`
	}
	if err := json.Unmarshal([]byte(bodies[1]), &replay); err != nil || replay.Project != "aicode-consumers" {
		t.Fatalf("second envelope: %v %s", err, bodies[1])
	}
	if !strings.Contains(bodies[1], `"thoughtSignature":"sig-1"`) || !strings.Contains(bodies[1], `"functionResponse"`) || strings.Contains(bodies[1], "Checking the files.") {
		t.Fatalf("the signature was not replayed or thought text leaked back: %s", bodies[1])
	}
	for _, agent := range agents {
		if !strings.HasPrefix(agent, "antigravity/hub/") {
			t.Fatalf("User-Agent: %q", agent)
		}
	}
	key, err := store.GetAPIKey(ctx, "wire-key")
	if err != nil || key.Limits[0].CurrentValue != 115 { // (40+10) + (60+5) tokens
		t.Fatalf("Antigravity usage settled incorrectly: %+v %v", key.Limits, err)
	}

	cloud.set("quota")
	status, body = post("thread", `{"model":"gemini-3.8-flash-high","stream":true,`+tools+`,"input":[{"role":"user","content":"list files"},{"type":"function_call","call_id":"call_ls","name":"shell","arguments":"{\"cmd\":\"ls\"}"},{"type":"function_call_output","call_id":"call_ls","output":"a.txt"},{"role":"user","content":"and again"}]}`)
	if status != http.StatusTooManyRequests || !strings.Contains(body, "usage_limit_reached") {
		t.Fatalf("quota on the sticky Antigravity account: %d %s", status, body)
	}

	if strings.Contains(fmt.Sprint(bodies), "synthetic-cloud-refresh") {
		t.Fatal("the refresh token reached Cloud Code")
	}
}

// A TOS suspension deactivates the account, keeps the appeal link and takes
// the account out of routing.
func TestAntigravitySuspensionTakesTheAccountOut(t *testing.T) {
	ctx := context.Background()
	route := newAntigravityRoute(t)
	cloud, store, post, source := route.cloud, route.store, route.post, route.source
	var status int
	var body string
	cloud.set("tos")
	status, body = post("fresh", `{"model":"gemini-3.8-flash-high","stream":true,"input":[{"role":"user","content":"hello"}]}`)
	if status != http.StatusForbidden || !strings.Contains(body, "account_suspended") {
		t.Fatalf("suspension: %d %s", status, body)
	}
	account, err := store.GetAccount(ctx, source.ID)
	if err != nil || account.Status != domain.AccountDeactivated || account.DeactivationReason != "tos_violation" {
		t.Fatalf("suspended account state: %+v %v", account, err)
	}
	stored, err := store.GetModelSource(ctx, source.ID)
	if err != nil || stored.ProviderConfig.Antigravity == nil || stored.ProviderConfig.Antigravity.ActionURL != "https://forms.gle/synthetic-appeal" {
		t.Fatalf("appeal link not kept: %+v %v", stored.ProviderConfig.Antigravity, err)
	}
	before, _ := cloud.requests()
	status, body = post("later", `{"model":"gemini-3.8-flash-high","stream":true,"input":[{"role":"user","content":"hello again"}]}`)
	after, _ := cloud.requests()
	if status < 400 || len(after) != len(before) {
		t.Fatalf("a suspended account was used again: %d calls %d->%d %s", status, len(before), len(after), body)
	}
}
