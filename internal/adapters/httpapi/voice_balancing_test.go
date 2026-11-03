package httpapi_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

// The wire account has no ChatGPT account id, so the stub sees it as "".
const (
	voiceFirst  = ""
	voiceSecond = "chatgpt-b"
)

// addVoiceAccount adds a second subscription account eligible for the wire
// key; its id sorts after the wire account's.
func (f liveFixture) addVoiceAccount(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if err := f.store.SaveAccount(ctx, domain.Account{ID: "wire-account-b", Kind: domain.AccountChatGPT, Provider: "openai", PlanType: "plus",
		Email: "second@example.invalid", Status: domain.AccountActive, RoutingPolicy: "normal", ChatGPTAccountID: voiceSecond, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	credential, err := f.store.GetAccountCredential(ctx, "wire-account")
	if err != nil {
		t.Fatal(err)
	}
	credential.AccountID = "wire-account-b"
	if err := f.store.SaveAccountCredential(ctx, credential); err != nil {
		t.Fatal(err)
	}
}

func sameAccounts(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func liveCall(t *testing.T, f liveFixture) *http.Response {
	t.Helper()
	return f.post(t, "/v1/live/calls", "application/sdp", "v=offer\r\n", "synthetic-key")
}

func TestLiveCallMovesToTheNextAccountOnALimitRefusal(t *testing.T) {
	f := newLiveFixture(t, application.LiveCallConfig{}, liveScript{frames: []string{liveStartedFrame()}, ending: liveHolds})
	f.addVoiceAccount(t)
	f.live.set(func(x *liveStub) { x.refuseAccounts, x.retryAfter = map[string]int{voiceFirst: 429}, "600" })
	if res := liveCall(t, f); res.StatusCode != http.StatusCreated || res.Header.Get("Location") != "/v1/live/rtc_live" {
		t.Fatalf("call: %d %v", res.StatusCode, res.Header)
	}
	if calls, _ := f.live.accounts(); !sameAccounts(calls, voiceFirst, voiceSecond) {
		t.Fatalf("call attempts %q", calls)
	}
	// The refused account's reservation went back; only the call's remains.
	if cost := f.keyCost(t); cost != 500_000 {
		t.Fatalf("reserved %d, want one call's 500000", cost)
	}
	if res := liveCall(t, f); res.StatusCode != http.StatusCreated {
		t.Fatalf("second call: %d", res.StatusCode)
	}
	if calls, _ := f.live.accounts(); !sameAccounts(calls, voiceFirst, voiceSecond, voiceSecond) {
		t.Fatalf("the refused account was tried again: %q", calls)
	}
}

func TestRealtimeVoiceMovesOnlyOnALimitRefusal(t *testing.T) {
	t.Run("call", func(t *testing.T) {
		f := newLiveFixture(t, application.LiveCallConfig{})
		f.addVoiceAccount(t)
		f.live.set(func(x *liveStub) { x.refuseAccounts = map[string]int{voiceFirst: 429} })
		res := f.post(t, "/v1/realtime/calls", "application/sdp", "v=offer\r\n", "synthetic-key")
		if res.StatusCode != http.StatusCreated || res.Header.Get("Location") != "/v1/realtime/calls/rtc_live" {
			t.Fatalf("realtime call: %d %v", res.StatusCode, res.Header)
		}
		if calls, _ := f.live.accounts(); !sameAccounts(calls, voiceFirst, voiceSecond) {
			t.Fatalf("realtime call attempts %q", calls)
		}
	})
	t.Run("session", func(t *testing.T) {
		f := newLiveFixture(t, application.LiveCallConfig{})
		f.addVoiceAccount(t)
		f.live.set(func(x *liveStub) { x.refuseAccounts = map[string]int{voiceFirst: 429} })
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(f.server.URL, "http")+"/v1/realtime?model=gpt-realtime",
			&websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer synthetic-key"}}})
		if err != nil {
			t.Fatal(err)
		}
		if got := readRealtimeEvent(t, conn); got != "session.created" {
			t.Fatalf("first session event %q", got)
		}
		_ = conn.Close(websocket.StatusNormalClosure, "")
		if _, sessions := f.live.accounts(); !sameAccounts(sessions, voiceFirst, voiceSecond) {
			t.Fatalf("session dials %q", sessions)
		}
	})
	t.Run("other refusals stay", func(t *testing.T) {
		f := newLiveFixture(t, application.LiveCallConfig{})
		f.addVoiceAccount(t)
		f.live.set(func(x *liveStub) { x.refuseAccounts = map[string]int{voiceFirst: 502} })
		if res := f.post(t, "/v1/realtime/calls", "application/sdp", "v=offer\r\n", "synthetic-key"); res.StatusCode != http.StatusBadGateway {
			t.Fatalf("realtime call: %d", res.StatusCode)
		}
		if res := liveCall(t, f); res.StatusCode != http.StatusBadGateway {
			t.Fatalf("live call: %d", res.StatusCode)
		}
		if calls, _ := f.live.accounts(); !sameAccounts(calls, voiceFirst, voiceFirst) {
			t.Fatalf("a failure that may have run moved on: %q", calls)
		}
	})
}

func TestVoiceLimitOnEveryAccountIsRefusedBeforeDispatch(t *testing.T) {
	f := newLiveFixture(t, application.LiveCallConfig{})
	f.addVoiceAccount(t)
	f.live.set(func(x *liveStub) {
		x.refuseAccounts, x.retryAfter = map[string]int{voiceFirst: 429, voiceSecond: 429}, "600"
	})
	res := liveCall(t, f)
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusTooManyRequests || res.Header.Get("Retry-After") != "600" || !strings.Contains(string(body), "realtime_call_unavailable") ||
		strings.Contains(string(body), "private refusal text") {
		t.Fatalf("last refusal: %d %v %s", res.StatusCode, res.Header, body)
	}
	if calls, _ := f.live.accounts(); !sameAccounts(calls, voiceFirst, voiceSecond) || f.keyCost(t) != 0 {
		t.Fatalf("attempts %q, cost %d", calls, f.keyCost(t))
	}
	res = liveCall(t, f)
	body, _ = io.ReadAll(res.Body)
	if res.StatusCode != http.StatusTooManyRequests || !strings.Contains(string(body), "voice_limit_reached") ||
		(res.Header.Get("Retry-After") != "600" && res.Header.Get("Retry-After") != "599") {
		t.Fatalf("cooling down: %d %v %s", res.StatusCode, res.Header, body)
	}
	if calls, _ := f.live.accounts(); len(calls) != 2 || f.keyCost(t) != 0 {
		t.Fatalf("a cooled-down request was dispatched or reserved: %q, cost %d", calls, f.keyCost(t))
	}
}

func TestLiveCallsSpreadAndHeedVoiceWarnings(t *testing.T) {
	t.Run("spread", func(t *testing.T) {
		// Usage events without a voice limit warning leave the ranking to load.
		// Only the first account reports usage, so a null status counted as a
		// warning would push its next call to the other account.
		f := newLiveFixture(t, application.LiveCallConfig{},
			liveScript{frames: []string{liveStartedFrame(), liveUsageFrame(1000)}, ending: liveHolds},
			liveScript{frames: []string{liveStartedFrame()}, ending: liveHolds})
		f.addVoiceAccount(t)
		for i := range 3 {
			if res := liveCall(t, f); res.StatusCode != http.StatusCreated {
				t.Fatalf("call: %d", res.StatusCode)
			}
			f.live.waitWritten(t, 1)
			if i == 0 {
				time.Sleep(100 * time.Millisecond) // lets the observer read its usage event
			}
		}
		if calls, _ := f.live.accounts(); !sameAccounts(calls, voiceFirst, voiceSecond, voiceFirst) {
			t.Fatalf("calls did not spread by load: %q", calls)
		}
	})
	t.Run("warning", func(t *testing.T) {
		warning := `{"type":"session.usage.updated","usage":{"audio_duration_ms":1000,"backend_model_usage":[]},"usage_limit":{"status":"approaching","reset_seconds":3600}}`
		f := newLiveFixture(t, application.LiveCallConfig{},
			liveScript{frames: []string{liveStartedFrame(), warning}, ending: liveHolds},
			liveScript{frames: []string{liveStartedFrame()}, ending: liveHolds})
		f.addVoiceAccount(t)
		if res := liveCall(t, f); res.StatusCode != http.StatusCreated {
			t.Fatalf("first call: %d", res.StatusCode)
		}
		f.live.waitWritten(t, 1)
		time.Sleep(100 * time.Millisecond) // lets the observer read the warning on loopback
		for range 3 {
			if res := liveCall(t, f); res.StatusCode != http.StatusCreated {
				t.Fatalf("call: %d", res.StatusCode)
			}
		}
		// The warned account stays last even once the other one is busier.
		if calls, _ := f.live.accounts(); !sameAccounts(calls, voiceFirst, voiceSecond, voiceSecond, voiceSecond) {
			t.Fatalf("calls went to the warned account: %q", calls)
		}
	})
}
