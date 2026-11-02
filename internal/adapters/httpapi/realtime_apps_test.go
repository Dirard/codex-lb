package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"codex-lb/internal/adapters/httpapi"
	"codex-lb/internal/adapters/provider"
	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

// realtimeAppUsage is a response.done usage as gpt-realtime reported it on
// 2026-11-03, with audio added.
const realtimeAppUsage = `{"input_tokens":120,"output_tokens":60,"input_token_details":{"text_tokens":20,"audio_tokens":100,"cached_tokens":0},"output_token_details":{"text_tokens":10,"audio_tokens":50}}`

// realtimeAppStub is a loopback OpenAI Realtime: sessions on /v1/realtime and
// the subscription backend's call creation, which takes JSON only.
type realtimeAppStub struct {
	mu       sync.Mutex
	server   *httptest.Server
	dials    []http.Header
	models   []string
	messages []string
	calls    []string
}

func newRealtimeAppStub(t *testing.T) *realtimeAppStub {
	t.Helper()
	stub := &realtimeAppStub{}
	stub.server = httptest.NewServer(http.HandlerFunc(stub.serve))
	t.Cleanup(stub.server.Close)
	return stub
}

func (x *realtimeAppStub) serve(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/codex/realtime/calls":
		body, _ := io.ReadAll(r.Body)
		x.mu.Lock()
		x.calls = append(x.calls, r.Header.Get("Content-Type")+" "+string(body))
		x.mu.Unlock()
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"detail":"Unsupported content type"}`)
			return
		}
		w.Header().Set("Location", "/v1/realtime/calls/rtc_app")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(201)
		_, _ = io.WriteString(w, "v=answer\r\n")
	case r.URL.Path == "/v1/realtime":
		model := r.URL.Query().Get("model")
		x.mu.Lock()
		x.dials, x.models = append(x.dials, r.Header.Clone()), append(x.models, model)
		x.mu.Unlock()
		if model == "refused-model" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":"private refusal text"}`)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx := r.Context()
		_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"session.created","session":{"type":"realtime","model":"`+model+`"}}`))
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			x.mu.Lock()
			x.messages = append(x.messages, string(data))
			x.mu.Unlock()
			var event struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal(data, &event)
			switch event.Type {
			case "session.update":
				_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"session.updated","session":{"type":"realtime"}}`))
			case "response.create":
				_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"response.output_text.delta","delta":"Pong."}`))
				_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"response.done","response":{"status":"completed","usage":`+realtimeAppUsage+`}}`))
			}
		}
	default:
		w.WriteHeader(404)
	}
}

func (x *realtimeAppStub) snapshot() ([]http.Header, []string, []string, []string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	return append([]http.Header(nil), x.dials...), append([]string(nil), x.models...), append([]string(nil), x.messages...), append([]string(nil), x.calls...)
}

type realtimeAppFixture struct {
	stub   *realtimeAppStub
	store  *sqlite.Store
	server *httptest.Server
}

func newRealtimeAppFixture(t *testing.T) realtimeAppFixture {
	t.Helper()
	stub := newRealtimeAppStub(t)
	_, store, proxy := wireFixtureWithProxy(t, nil)
	adapter := provider.New(store, fixedTokenSource{}, nil, provider.Config{HTTPClient: stub.server.Client(), ChatGPTBaseURL: stub.server.URL + "/codex"})
	t.Cleanup(func() { _ = adapter.Close() })
	ops := application.NewCodexOperations(store, store, adapter, time.Hour)
	ops.ConfigureAdmission(proxy)
	ops.ConfigureAccountSelection(proxy)
	ctx := context.Background()
	key, err := store.GetAPIKey(ctx, "wire-key")
	if err != nil {
		t.Fatal(err)
	}
	key.Limits = append(key.Limits, domain.LimitRule{Type: domain.LimitCostUSD, Window: domain.WindowWeekly, MaxValue: 100_000_000})
	if err := store.SaveAPIKey(ctx, key, time.Now()); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.NewCodexOperationsHandler(store, ops, nil))
	t.Cleanup(server.Close)
	return realtimeAppFixture{stub: stub, store: store, server: server}
}

func (f realtimeAppFixture) post(t *testing.T, path, contentType, body, token string) *http.Response {
	t.Helper()
	r, err := http.NewRequest(http.MethodPost, f.server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", contentType)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := f.server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	return res
}

func (f realtimeAppFixture) dial(t *testing.T, query string, header http.Header, subprotocols ...string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return websocket.Dial(ctx, "ws"+strings.TrimPrefix(f.server.URL, "http")+"/v1/realtime"+query, &websocket.DialOptions{HTTPHeader: header, Subprotocols: subprotocols})
}

// charged waits for the session's settlement and returns what the key was
// charged: cost in microdollars and total tokens.
func (f realtimeAppFixture) charged(t *testing.T) (int64, int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		key, err := f.store.GetAPIKey(context.Background(), "wire-key")
		if err != nil {
			t.Fatal(err)
		}
		pending, err := f.store.ListReservationsNeedingReconciliation(context.Background(), "", 10)
		if err != nil {
			t.Fatal(err)
		}
		var cost, tokens int64
		for _, limit := range key.Limits {
			switch limit.Type {
			case domain.LimitCostUSD:
				cost = limit.CurrentValue
			case domain.LimitTotalTokens:
				tokens = limit.CurrentValue
			}
		}
		totals, err := f.store.UsageTotals(context.Background(), "wire-key", "wire-account")
		if err != nil {
			t.Fatal(err)
		}
		if totals.RequestCount > 0 && len(pending) == 0 {
			return cost, tokens
		}
		if time.Now().After(deadline) {
			t.Fatalf("session was not settled: %+v pending=%d", totals, len(pending))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func readRealtimeEvent(t *testing.T, conn *websocket.Conn) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var event struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(data, &event)
	return event.Type
}

func TestRealtimeAppSessionRelaysAndSettlesOnce(t *testing.T) {
	f := newRealtimeAppFixture(t)
	conn, _, err := f.dial(t, "?model=gpt-realtime", http.Header{"Authorization": {"Bearer synthetic-key"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := readRealtimeEvent(t, conn); got != "session.created" {
		t.Fatalf("first event %q", got)
	}
	ctx := context.Background()
	for _, message := range []string{
		`{"type":"session.update","session":{"type":"realtime","instructions":"One word."}}`,
		`{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[{"type":"input_text","text":"Say pong."}]}}`,
		`{"type":"response.create"}`,
	} {
		if err := conn.Write(ctx, websocket.MessageText, []byte(message)); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []string{"session.updated", "response.output_text.delta", "response.done"} {
		if got := readRealtimeEvent(t, conn); got != want {
			t.Fatalf("event %q, want %q", got, want)
		}
	}
	_ = conn.Close(websocket.StatusNormalClosure, "")
	cost, tokens := f.charged(t)
	var usage domain.RealtimeUsage
	_ = json.Unmarshal([]byte(realtimeAppUsage), &usage)
	want, _ := usage.Amount("gpt-realtime")
	if cost != want.CostMicrodollars || tokens != 180 {
		t.Fatalf("charged cost=%d tokens=%d, want cost=%d tokens=180", cost, tokens, want.CostMicrodollars)
	}
	dials, models, messages, _ := f.stub.snapshot()
	if len(dials) != 1 || models[0] != "gpt-realtime" || dials[0].Get("Authorization") != "Bearer synthetic-test-token" ||
		dials[0].Get("Originator") != "codex_cli_rs" || len(messages) != 3 {
		t.Fatalf("upstream dial %v %v messages=%d", dials, models, len(messages))
	}
}

func TestRealtimeAppSessionRefusesBeforeTheUpgrade(t *testing.T) {
	f := newRealtimeAppFixture(t)
	for name, test := range map[string]struct {
		query  string
		header http.Header
		status int
	}{
		"no key":         {"?model=gpt-realtime", nil, 401},
		"no model":       {"", http.Header{"Authorization": {"Bearer synthetic-key"}}, 400},
		"bad model":      {"?model=" + strings.Repeat("m", 129), http.Header{"Authorization": {"Bearer synthetic-key"}}, 400},
		"unknown secret": {"?model=gpt-realtime", http.Header{"Authorization": {"Bearer ek_unknown"}}, 401},
	} {
		_, res, err := f.dial(t, test.query, test.header)
		if err == nil || res == nil || res.StatusCode != test.status {
			t.Fatalf("%s: %v %v", name, res, err)
		}
	}
	if dials, _, _, _ := f.stub.snapshot(); len(dials) != 0 {
		t.Fatalf("refused sessions dialed upstream: %d", len(dials))
	}
	conn, _, err := f.dial(t, "?model=refused-model", http.Header{"Authorization": {"Bearer synthetic-key"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err = conn.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusInternalError || strings.Contains(err.Error(), "private refusal") ||
		!strings.Contains(err.Error(), "realtime_session_unavailable") {
		t.Fatalf("refusal close: %v", err)
	}
	if cost, tokens := f.charged(t); cost != 0 || tokens != 0 {
		t.Fatalf("a refused session was charged: cost=%d tokens=%d", cost, tokens)
	}
}

func TestRealtimeAppClientSecretServesBrowsers(t *testing.T) {
	f := newRealtimeAppFixture(t)
	res := f.post(t, "/v1/realtime/client_secrets", "application/json",
		`{"expires_after":{"anchor":"created_at","seconds":300},"session":{"type":"realtime","instructions":"Be brief."}}`, "synthetic-key")
	var secret struct {
		Value     string          `json:"value"`
		ExpiresAt int64           `json:"expires_at"`
		Session   json.RawMessage `json:"session"`
	}
	if err := json.NewDecoder(res.Body).Decode(&secret); err != nil || res.StatusCode != 200 || !strings.HasPrefix(secret.Value, "ek_") ||
		secret.ExpiresAt < time.Now().Add(290*time.Second).Unix() || secret.ExpiresAt > time.Now().Add(310*time.Second).Unix() {
		t.Fatalf("secret: %d %+v %v", res.StatusCode, secret, err)
	}
	if res := f.post(t, "/v1/realtime/client_secrets", "application/json", `{}`, secret.Value); res.StatusCode != 401 {
		t.Fatalf("a secret minted another: %d", res.StatusCode)
	}
	if res := f.post(t, "/v1/realtime/client_secrets", "application/json", `{"expires_after":{"seconds":5}}`, "synthetic-key"); res.StatusCode != 400 {
		t.Fatalf("too short a lifetime: %d", res.StatusCode)
	}
	conn, response, err := f.dial(t, "?model=gpt-realtime", nil, "realtime", "openai-insecure-api-key."+secret.Value)
	if err != nil {
		t.Fatalf("browser dial: %v %v", response, err)
	}
	if conn.Subprotocol() != "realtime" {
		t.Fatalf("subprotocol %q", conn.Subprotocol())
	}
	if got := readRealtimeEvent(t, conn); got != "session.created" {
		t.Fatalf("first event %q", got)
	}
	_ = conn.Close(websocket.StatusNormalClosure, "")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, _, messages, _ := f.stub.snapshot(); len(messages) == 1 {
			if messages[0] != `{"type":"session.update","session":{"type":"realtime","instructions":"Be brief."}}` {
				t.Fatalf("first upstream message: %s", messages[0])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the secret's session never reached the upstream")
		}
		time.Sleep(20 * time.Millisecond)
	}
	call := f.post(t, "/v1/realtime/calls", "application/sdp", "v=offer\r\n", secret.Value)
	answer, _ := io.ReadAll(call.Body)
	if call.StatusCode != 201 || string(answer) != "v=answer\r\n" || call.Header.Get("Content-Type") != "application/sdp" ||
		call.Header.Get("Location") != "/v1/realtime/calls/rtc_app" {
		t.Fatalf("browser call: %d %q %v", call.StatusCode, answer, call.Header)
	}
	_, _, _, calls := f.stub.snapshot()
	if len(calls) != 1 || calls[0] != `application/json {"sdp":"v=offer\r\n","session":{"type":"realtime","instructions":"Be brief."}}` {
		t.Fatalf("backend call: %q", calls)
	}
	if res := f.post(t, "/v1/responses", "application/json", `{}`, secret.Value); res.StatusCode != 401 && res.StatusCode != 404 {
		t.Fatalf("a secret reached another route: %d", res.StatusCode)
	}
	key, err := f.store.GetAPIKey(context.Background(), "wire-key")
	if err != nil {
		t.Fatal(err)
	}
	key.IsActive = false
	if err := f.store.SaveAPIKey(context.Background(), key, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, res, err := f.dial(t, "?model=gpt-realtime", http.Header{"Authorization": {"Bearer " + secret.Value}}); err == nil || res.StatusCode != 401 {
		t.Fatalf("a secret outlived its key: %v %v", res, err)
	}
}

func TestRealtimeAppCallsConvertOffers(t *testing.T) {
	f := newRealtimeAppFixture(t)
	multipart := "--b\r\nContent-Disposition: form-data; name=\"sdp\"\r\n\r\nv=offer\r\n--b\r\nContent-Disposition: form-data; name=\"session\"\r\n\r\n{\"type\":\"realtime\",\"audio\":{\"output\":{\"voice\":\"cedar\"}}}\r\n--b--\r\n"
	for _, test := range []struct {
		path, media, body, want string
	}{
		{"/v1/realtime/calls?model=gpt-realtime-mini", "application/sdp", "v=offer\r\n", `{"sdp":"v=offer\r\n","session":{"model":"gpt-realtime-mini","type":"realtime"}}`},
		{"/v1/realtime/calls/", "multipart/form-data; boundary=b", multipart, `{"sdp":"v=offer","session":{"type":"realtime","audio":{"output":{"voice":"cedar"}}}}`},
		{"/v1/realtime/calls", "application/json", `{"sdp":"v=offer","session":{"type":"realtime"}}`, `{"sdp":"v=offer","session":{"type":"realtime"}}`},
	} {
		res := f.post(t, test.path, test.media, test.body, "synthetic-key")
		if res.StatusCode != 201 || res.Header.Get("Location") != "/v1/realtime/calls/rtc_app" {
			t.Fatalf("%s %s: %d", test.path, test.media, res.StatusCode)
		}
		_, _, _, calls := f.stub.snapshot()
		if got := calls[len(calls)-1]; got != "application/json "+test.want {
			t.Fatalf("%s: backend got %q", test.media, got)
		}
	}
	if res := f.post(t, "/v1/realtime/calls", "text/plain", "v=offer", "synthetic-key"); res.StatusCode != 415 {
		t.Fatalf("text offer: %d", res.StatusCode)
	}
	if _, _, _, calls := f.stub.snapshot(); len(calls) != 3 {
		t.Fatalf("a refused offer was dispatched: %d", len(calls))
	}
	owner, err := f.store.GetCodexResourceOwner(context.Background(), application.CodexResourceRealtime, "rtc_app", "wire-key", time.Now())
	if err != nil || owner.AccountID != "wire-account" {
		t.Fatalf("call owner: %+v %v", owner, err)
	}
}
