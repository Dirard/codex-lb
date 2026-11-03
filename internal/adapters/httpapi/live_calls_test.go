package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/adapters/httpapi"
	"codex-lb/internal/adapters/provider"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

// liveEnding is how a stub sideband connection ends after its frames.
type liveEnding int

const (
	liveCloses liveEnding = iota // a normal close
	liveDrops                    // an abrupt drop
	liveHolds                    // stays open until the peer leaves
)

// liveScript is what one accepted sideband connection sends: its frames and
// how it ends. Frames follow the shapes GPT-Live sent on 2026-11-03.
type liveScript struct {
	frames []string
	ending liveEnding
}

func liveUsageFrame(ms int) string {
	return `{"type":"session.usage.updated","usage":{"audio_duration_ms":` + strconv.Itoa(ms) + `,"backend_model_usage":[]},"usage_limit":{"status":null,"reset_seconds":null}}`
}

func liveClosedFrame(ms int) string {
	return `{"type":"session.closed","reason":"client_request","usage":{"audio_duration_ms":` + strconv.Itoa(ms) + `,"backend_model_usage":[]}}`
}

func liveStartedFrame() string {
	return `{"type":"session.started","session":{"id":"rtc_live","status":"active","expires_at":` + strconv.FormatInt(time.Now().Add(2*time.Hour).Unix(), 10) + `}}`
}

// liveAudioFrame is a sideband audio reflection the observer must skip.
const liveAudioFrame = `{"type":"session.output_audio.delta","delta":"AAAAAAAA","start_ms":0,"end_ms":200}`

type liveDial struct {
	alpha, authorization string
	messages             []string
}

// liveStub is a loopback GPT-Live: the subscription backend's call creation
// and the sideband at /v1/live/<id>.
type liveStub struct {
	mu         sync.Mutex
	server     *httptest.Server
	calls      []*http.Request
	bodies     []string
	refuseCall bool
	refuse     int  // sideband dials refused with 404 before one is accepted
	gone       bool // every sideband dial is refused with 404
	refused    int
	dials      []*liveDial
	scripts    []liveScript // per accepted connection; the last one repeats
	written    chan struct{}
}

func newLiveStub(t *testing.T, scripts ...liveScript) *liveStub {
	t.Helper()
	stub := &liveStub{scripts: scripts, written: make(chan struct{}, 16)}
	stub.server = httptest.NewServer(http.HandlerFunc(stub.serve))
	t.Cleanup(stub.server.Close)
	return stub
}

func (x *liveStub) serve(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/codex/realtime/calls":
		body, _ := io.ReadAll(r.Body)
		x.mu.Lock()
		x.calls, x.bodies = append(x.calls, r.Clone(context.Background())), append(x.bodies, string(body))
		refuse := x.refuseCall
		x.mu.Unlock()
		if refuse {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"detail":"private refusal text"}`)
			return
		}
		w.Header().Set("Location", "/v1/realtime/calls/rtc_live")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, "v=answer\r\n")
	case r.URL.Path == "/v1/live/rtc_live":
		x.mu.Lock()
		if x.gone || x.refuse > 0 {
			x.refuse--
			x.refused++
			x.mu.Unlock()
			w.WriteHeader(http.StatusNotFound)
			return
		}
		dial := &liveDial{alpha: r.Header.Get("OpenAI-Alpha"), authorization: r.Header.Get("Authorization")}
		x.dials = append(x.dials, dial)
		script := liveScript{ending: liveHolds}
		if len(x.scripts) != 0 {
			script = x.scripts[min(len(x.dials), len(x.scripts))-1]
		}
		x.mu.Unlock()
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx := r.Context()
		read := make(chan struct{})
		go func() {
			defer close(read)
			for {
				_, data, err := conn.Read(ctx)
				if err != nil {
					return
				}
				x.mu.Lock()
				dial.messages = append(dial.messages, string(data))
				x.mu.Unlock()
			}
		}()
		for _, frame := range script.frames {
			if conn.Write(ctx, websocket.MessageText, []byte(frame)) != nil {
				return
			}
		}
		x.written <- struct{}{}
		switch script.ending {
		case liveCloses:
			_ = conn.Close(websocket.StatusNormalClosure, "")
		case liveDrops:
			_ = conn.CloseNow()
		case liveHolds:
			<-read
		}
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// set changes the stub's behavior under its lock.
func (x *liveStub) set(change func(*liveStub)) {
	x.mu.Lock()
	defer x.mu.Unlock()
	change(x)
}

func (x *liveStub) snapshot() (calls []*http.Request, bodies []string, dials []liveDial, refused int) {
	x.mu.Lock()
	defer x.mu.Unlock()
	for _, dial := range x.dials {
		dials = append(dials, liveDial{alpha: dial.alpha, authorization: dial.authorization, messages: append([]string(nil), dial.messages...)})
	}
	return append([]*http.Request(nil), x.calls...), append([]string(nil), x.bodies...), dials, x.refused
}

// waitWritten waits until n sideband connections sent their frames.
func (x *liveStub) waitWritten(t *testing.T, n int) {
	t.Helper()
	for range n {
		select {
		case <-x.written:
		case <-time.After(5 * time.Second):
			t.Fatal("the sideband frames were not sent")
		}
	}
}

type liveFixture struct {
	realtimeAppFixture
	live *liveStub
	ops  *application.CodexOperations
}

func newLiveFixture(t *testing.T, config application.LiveCallConfig, scripts ...liveScript) liveFixture {
	t.Helper()
	stub := newLiveStub(t, scripts...)
	_, store, proxy := wireFixtureWithProxy(t, nil)
	adapter := provider.New(store, fixedTokenSource{}, nil, provider.Config{HTTPClient: stub.server.Client(), ChatGPTBaseURL: stub.server.URL + "/codex"})
	t.Cleanup(func() { _ = adapter.Close() })
	ops := application.NewCodexOperations(store, store, adapter, time.Hour)
	ops.ConfigureAdmission(proxy)
	ops.ConfigureAccountSelection(proxy)
	if config.AttachWindow == 0 {
		config.AttachWindow = 5 * time.Second
	}
	config.RetryMin, config.RetryMax = 10*time.Millisecond, 40*time.Millisecond
	ops.ConfigureLiveCalls(config)
	// Runs before the stub closes, so held sidebands end with their observers.
	t.Cleanup(ops.CloseLiveCalls)
	ctx := context.Background()
	key, err := store.GetAPIKey(ctx, "wire-key")
	if err != nil {
		t.Fatal(err)
	}
	key.Limits = append(key.Limits, domain.LimitRule{Type: domain.LimitCostUSD, Window: domain.WindowWeekly, MaxValue: 100_000_000})
	if err := store.SaveAPIKey(ctx, key, time.Now()); err != nil {
		t.Fatal(err)
	}
	vault, err := credentials.Open(filepath.Join(t.TempDir(), "admin-key"), true)
	if err != nil {
		t.Fatal(err)
	}
	api := httpapi.New(store, vault, httpapi.Config{BootstrapToken: "synthetic-bootstrap-token-0123456789abcdef"}, nil)
	server := httptest.NewServer(api.Handler(httpapi.NewCodexOperationsHandler(store, ops, nil), nil))
	t.Cleanup(server.Close)
	return liveFixture{realtimeAppFixture: realtimeAppFixture{store: store, server: server}, live: stub, ops: ops}
}

// settled waits for the call's live_session row and returns it with the
// key's current cost, which no longer holds the call's reservation.
func (f liveFixture) settled(t *testing.T) (domain.RequestLogEntry, int64) {
	t.Helper()
	return f.settledWithin(t, 5*time.Second)
}

func (f liveFixture) settledWithin(t *testing.T, wait time.Duration) (domain.RequestLogEntry, int64) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for {
		logs, err := f.store.ListRequestLogs(context.Background(), domain.RequestLogFilter{Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range logs.Requests {
			if entry.RequestKind == "live_session" {
				return entry, f.keyCost(t)
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the live call was not settled: %+v", logs.Requests)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (f liveFixture) keyCost(t *testing.T) int64 {
	t.Helper()
	key, err := f.store.GetAPIKey(context.Background(), "wire-key")
	if err != nil {
		t.Fatal(err)
	}
	for _, limit := range key.Limits {
		if limit.Type == domain.LimitCostUSD {
			return limit.CurrentValue
		}
	}
	return 0
}

func errorCode(entry domain.RequestLogEntry) string {
	if entry.ErrorCode == nil {
		return ""
	}
	return *entry.ErrorCode
}

func TestLiveCallFromBrowserIsCreatedAndMetered(t *testing.T) {
	f := newLiveFixture(t, application.LiveCallConfig{}, liveScript{
		frames: []string{liveStartedFrame(), liveAudioFrame, liveUsageFrame(30_000), liveAudioFrame, liveUsageFrame(45_000), liveClosedFrame(60_000)},
		ending: liveCloses,
	})
	f.live.set(func(x *liveStub) { x.refuse = 1 }) // Media is still connecting when the observer first attaches.
	res := f.post(t, "/v1/live/client_secrets", "application/json", `{"session":{"instructions":"Be brief.","audio":{"output":{"voice":"cove"}}}}`, "synthetic-key")
	var secret struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(res.Body).Decode(&secret); err != nil || res.StatusCode != 200 || !strings.HasPrefix(secret.Value, "ek_") {
		t.Fatalf("secret: %d %v", res.StatusCode, err)
	}
	preflight := f.browserRequest(t, http.MethodOptions, "/v1/live/calls", "", "", http.Header{
		"Access-Control-Request-Method": {"POST"}, "Access-Control-Request-Headers": {"authorization,content-type"},
	})
	if preflight.StatusCode != http.StatusNoContent || preflight.Header.Get("Access-Control-Allow-Origin") != "*" ||
		preflight.Header.Get("Access-Control-Allow-Headers") != "Authorization, Content-Type" {
		t.Fatalf("preflight: %d %v", preflight.StatusCode, preflight.Header)
	}
	call := f.browserRequest(t, http.MethodPost, "/v1/live/calls", "application/sdp", "v=offer\r\n", http.Header{"Authorization": {"Bearer " + secret.Value}})
	answer, _ := io.ReadAll(call.Body)
	if call.StatusCode != http.StatusCreated || call.Header.Get("Content-Type") != "application/sdp" || string(answer) != "v=answer\r\n" ||
		call.Header.Get("Location") != "/v1/live/rtc_live" || call.Header.Get("Access-Control-Allow-Origin") != "*" ||
		call.Header.Get("Access-Control-Expose-Headers") != "Location" {
		t.Fatalf("foreign page call: %d %v %q", call.StatusCode, call.Header, answer)
	}
	if res := f.browserRequest(t, http.MethodPost, "/v1/live/client_secrets", "application/json", `{}`, http.Header{"Authorization": {"Bearer synthetic-key"}}); res.StatusCode != http.StatusForbidden {
		t.Fatalf("a foreign page minted a secret: %d", res.StatusCode)
	}
	entry, cost := f.settled(t)
	if entry.Status != "success" || entry.CostUSD == nil || *entry.CostUSD != 0.05 || entry.Model != "gpt-live-1.1-codex" ||
		entry.Transport == nil || *entry.Transport != "webrtc" || cost != 50_000 {
		t.Fatalf("settled %+v cost=%d, want one success charging 50000 microdollars", entry, cost)
	}
	calls, bodies, dials, refused := f.live.snapshot()
	if len(calls) != 1 || calls[0].URL.RawQuery != "intent=quicksilver&architecture=avas" || calls[0].Header.Get("OpenAI-Alpha") != "quicksilver=v2" ||
		calls[0].Header.Get("Authorization") != "Bearer synthetic-test-token" || !strings.HasPrefix(calls[0].Header.Get("Content-Type"), "application/json") {
		t.Fatalf("backend call %+v", calls)
	}
	var offer struct {
		SDP     string `json:"sdp"`
		Session struct {
			Model        string          `json:"model"`
			Instructions string          `json:"instructions"`
			Delegation   json.RawMessage `json:"delegation"`
			Audio        struct {
				Output struct {
					Voice string `json:"voice"`
				} `json:"output"`
			} `json:"audio"`
		} `json:"session"`
	}
	if err := json.Unmarshal([]byte(bodies[0]), &offer); err != nil || offer.SDP != "v=offer\r\n" || offer.Session.Model != "gpt-live-1.1-codex" ||
		offer.Session.Instructions != "Be brief." || offer.Session.Audio.Output.Voice != "cove" || string(offer.Session.Delegation) != `{"type":"client"}` {
		t.Fatalf("backend offer %s: %v", bodies[0], err)
	}
	if refused != 1 || len(dials) != 1 || dials[0].alpha != "quicksilver=v2" || dials[0].authorization != "Bearer synthetic-test-token" || len(dials[0].messages) != 0 {
		t.Fatalf("observer dials %+v refused=%d", dials, refused)
	}
}

func TestLiveCallOffersAreCheckedBeforeDispatch(t *testing.T) {
	f := newLiveFixture(t, application.LiveCallConfig{}, liveScript{frames: []string{liveStartedFrame(), liveClosedFrame(0)}, ending: liveCloses})
	for _, refused := range []struct {
		contentType, body string
		status            int
	}{
		{"application/json", `{"sdp":"v=offer\r\n","session":{"model":"bad model!"}}`, 400},
		{"application/json", `{"sdp":"v=offer\r\n","session":["not","an","object"]}`, 400},
		{"application/json", `{"sdp":"","session":{}}`, 400},
		{"text/plain", "v=offer\r\n", 415},
	} {
		if res := f.post(t, "/v1/live/calls", refused.contentType, refused.body, "synthetic-key"); res.StatusCode != refused.status {
			t.Fatalf("%s %s: %d, want %d", refused.contentType, refused.body, res.StatusCode, refused.status)
		}
	}
	if calls, _, _, _ := f.live.snapshot(); len(calls) != 0 || f.keyCost(t) != 0 {
		t.Fatalf("a refused offer was dispatched or reserved: %d calls, cost %d", len(calls), f.keyCost(t))
	}
	res := f.post(t, "/v1/live/calls?model=ignored-model", "application/json",
		`{"sdp":"v=offer\r\n","session":{"model":"gpt-live-1-codex","delegation":{"type":"client","ack_filler":true}}}`, "synthetic-key")
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("json offer: %d", res.StatusCode)
	}
	_, bodies, _, _ := f.live.snapshot()
	if !strings.Contains(bodies[0], `"model":"gpt-live-1-codex"`) || !strings.Contains(bodies[0], `"ack_filler":true`) {
		t.Fatalf("present fields changed: %s", bodies[0])
	}
	if entry, cost := f.settled(t); entry.Status != "success" || entry.Model != "gpt-live-1-codex" || cost != 0 {
		t.Fatalf("settled %+v cost=%d", entry, cost)
	}
	// A null model counts as missing and takes the requested one.
	if res := f.post(t, "/v1/live/calls?model=gpt-live-1-codex", "application/json", `{"sdp":"v=offer\r\n","session":{"model":null}}`, "synthetic-key"); res.StatusCode != http.StatusCreated {
		t.Fatalf("null model offer: %d", res.StatusCode)
	}
	if _, bodies, _, _ := f.live.snapshot(); len(bodies) != 2 || !strings.Contains(bodies[1], `"model":"gpt-live-1-codex"`) {
		t.Fatalf("null model was sent as %v", bodies)
	}
}

func TestLiveCallRefusalReleasesReservation(t *testing.T) {
	f := newLiveFixture(t, application.LiveCallConfig{})
	f.live.set(func(x *liveStub) { x.refuseCall = true })
	res := f.post(t, "/v1/live/calls", "application/sdp", "v=offer\r\n", "synthetic-key")
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "realtime_call_unavailable") || strings.Contains(string(body), "private refusal text") {
		t.Fatalf("refusal: %d %s", res.StatusCode, body)
	}
	if cost := f.keyCost(t); cost != 0 {
		t.Fatalf("a refused call kept its reservation: %d", cost)
	}
	time.Sleep(50 * time.Millisecond)
	if _, _, dials, refused := f.live.snapshot(); len(dials) != 0 || refused != 0 {
		t.Fatalf("a refused call was observed: %d dials, %d refused", len(dials), refused)
	}
}

func TestLiveCallObserverReattachesAfterADrop(t *testing.T) {
	f := newLiveFixture(t, application.LiveCallConfig{},
		liveScript{frames: []string{liveStartedFrame(), liveUsageFrame(20_000)}, ending: liveDrops},
		// A reattached sideband replays the last seconds before the close.
		liveScript{frames: []string{liveStartedFrame(), liveUsageFrame(40_000), liveClosedFrame(60_000)}, ending: liveCloses})
	if res := f.post(t, "/v1/live/calls", "application/sdp", "v=offer\r\n", "synthetic-key"); res.StatusCode != http.StatusCreated {
		t.Fatalf("call: %d", res.StatusCode)
	}
	entry, cost := f.settled(t)
	if entry.Status != "success" || cost != 50_000 {
		t.Fatalf("settled %+v cost=%d", entry, cost)
	}
	if _, _, dials, _ := f.live.snapshot(); len(dials) != 2 {
		t.Fatalf("the observer did not reattach: %d dials", len(dials))
	}
}

func TestClosingLiveCallsChargesKnownUsage(t *testing.T) {
	f := newLiveFixture(t, application.LiveCallConfig{}, liveScript{frames: []string{liveStartedFrame(), liveUsageFrame(30_000)}, ending: liveHolds})
	if res := f.post(t, "/v1/live/calls", "application/sdp", "v=offer\r\n", "synthetic-key"); res.StatusCode != http.StatusCreated {
		t.Fatalf("call: %d", res.StatusCode)
	}
	f.live.waitWritten(t, 1)
	time.Sleep(100 * time.Millisecond) // lets the observer read the frames on loopback
	f.ops.CloseLiveCalls()
	// Closing joins the observers, so the settlement is already written.
	entry, cost := f.settledWithin(t, 0)
	if entry.Status != "cancelled" || errorCode(entry) != "live_usage_incomplete" || cost != 25_000 {
		t.Fatalf("settled %+v cost=%d, want cancelled with 25000 microdollars", entry, cost)
	}
	if res := f.post(t, "/v1/live/calls", "application/sdp", "v=offer\r\n", "synthetic-key"); res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("a call after close: %d", res.StatusCode)
	}
}

func TestLiveCallNeverAttachedChargesNothing(t *testing.T) {
	f := newLiveFixture(t, application.LiveCallConfig{AttachWindow: 300 * time.Millisecond})
	f.live.set(func(x *liveStub) { x.gone = true })
	if res := f.post(t, "/v1/live/calls", "application/sdp", "v=offer\r\n", "synthetic-key"); res.StatusCode != http.StatusCreated {
		t.Fatalf("call: %d", res.StatusCode)
	}
	entry, cost := f.settled(t)
	if entry.Status != "error" || errorCode(entry) != "live_call_unobserved" || cost != 0 {
		t.Fatalf("settled %+v cost=%d", entry, cost)
	}
	if _, _, _, refused := f.live.snapshot(); refused < 2 {
		t.Fatalf("the observer gave up without retrying: %d", refused)
	}
}

func TestLiveCallsBusyAreRefusedBeforeDispatch(t *testing.T) {
	f := newLiveFixture(t, application.LiveCallConfig{Observers: 1}, liveScript{frames: []string{liveStartedFrame()}, ending: liveHolds})
	if res := f.post(t, "/v1/live/calls", "application/sdp", "v=offer\r\n", "synthetic-key"); res.StatusCode != http.StatusCreated {
		t.Fatalf("first call: %d", res.StatusCode)
	}
	reserved := f.keyCost(t)
	res := f.post(t, "/v1/live/calls", "application/sdp", "v=offer\r\n", "synthetic-key")
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(body), "live_calls_busy") {
		t.Fatalf("second call: %d %s", res.StatusCode, body)
	}
	if calls, _, _, _ := f.live.snapshot(); len(calls) != 1 || f.keyCost(t) != reserved || reserved != 500_000 {
		t.Fatalf("a busy call was dispatched or reserved: %d calls, cost %d, first reservation %d", len(calls), f.keyCost(t), reserved)
	}
}

func TestAppLiveSidebandGetsFramelessAlpha(t *testing.T) {
	f := newLiveFixture(t, application.LiveCallConfig{}, liveScript{frames: []string{liveStartedFrame()}, ending: liveHolds})
	if res := f.post(t, "/v1/live/calls", "application/sdp", "v=offer\r\n", "synthetic-key"); res.StatusCode != http.StatusCreated {
		t.Fatalf("call: %d", res.StatusCode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(f.server.URL, "http")+"/v1/live/rtc_live",
		&websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer synthetic-key"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := readRealtimeEvent(t, conn); got != "session.started" {
		t.Fatalf("first app event %q", got)
	}
	const appMessage = `{"type":"delegation.context.append","delegation_item_id":"item_1","channel":"speakable","content":[{"type":"input_text","text":"Done."}]}`
	if err := conn.Write(ctx, websocket.MessageText, []byte(appMessage)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, _, dials, _ := f.live.snapshot()
		var app *liveDial
		for i := range dials {
			if len(dials[i].messages) != 0 {
				app = &dials[i]
			}
		}
		if app != nil {
			if app.alpha != "quicksilver=v2" || app.messages[0] != appMessage {
				t.Fatalf("app sideband %+v", *app)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the app message did not arrive: %+v", dials)
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = conn.Close(websocket.StatusNormalClosure, "")
}
