package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/adapters/provider"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
	"github.com/coder/websocket"
)

func interruptConnection(t *testing.T, ctx context.Context, server *httptest.Server, path string) *websocket.Conn {
	t.Helper()
	connection, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+path, &websocket.DialOptions{HTTPHeader: http.Header{
		"Authorization": {"Bearer synthetic-key"}, "User-Agent": {"codex_cli_rs/0.156.0"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { connection.CloseNow() })
	return connection
}

func interruptWrite(t *testing.T, ctx context.Context, connection *websocket.Conn, frame string) {
	t.Helper()
	if err := connection.Write(ctx, websocket.MessageText, []byte(frame)); err != nil {
		t.Fatal(err)
	}
}

func interruptRead(t *testing.T, ctx context.Context, connection *websocket.Conn) (string, []byte) {
	t.Helper()
	for {
		_, frame, err := connection.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var event struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(frame, &event); err != nil {
			t.Fatalf("invalid WebSocket event: %s: %v", frame, err)
		}
		if event.Type != "codex.keepalive" && event.Type != "response.in_progress" {
			return event.Type, frame
		}
	}
}

func interruptExpect(t *testing.T, ctx context.Context, connection *websocket.Conn, want string) []byte {
	t.Helper()
	kind, frame := interruptRead(t, ctx, connection)
	if kind != want {
		t.Fatalf("WebSocket event %s, want %s: %s", kind, want, frame)
	}
	return frame
}

func TestResponseInterruptRoutesAndContinuation(t *testing.T) {
	for _, path := range []string{"/backend-api/codex/responses", "/backend-api/codex/responses/", "/v1/responses", "/v1/responses/"} {
		t.Run(path, func(t *testing.T) {
			const firstID = "resp-interrupted"
			var calls atomic.Int32
			server, store := wireFixture(t, wireProvider(func(ctx context.Context, target application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
				if calls.Add(1) != 1 {
					return wireComplete("resp-follow-up", emit)
				}
				if target.OnInterruptReady == nil {
					return application.ResponseResult{}, errors.New("interrupt registration missing")
				}
				interrupts := make(chan string, 1)
				target.OnInterruptReady(interrupts)
				defer target.OnInterruptReady(nil)
				for _, event := range []application.ResponseEvent{
					{Type: "response.created", Data: []byte(`{"type":"response.created","response":{"id":"resp-interrupted","status":"in_progress"}}`)},
					{Type: "response.output_text.delta", Data: []byte(`{"type":"response.output_text.delta","delta":"partial"}`)},
				} {
					if err := emit(event); err != nil {
						return application.ResponseResult{}, err
					}
				}
				select {
				case id := <-interrupts:
					if id != firstID {
						return application.ResponseResult{}, fmt.Errorf("interrupt target = %q", id)
					}
				case <-ctx.Done():
					return application.ResponseResult{}, ctx.Err()
				}
				for _, event := range []application.ResponseEvent{
					{Type: "response.interrupt.accepted", Data: []byte(`{"type":"response.interrupt.accepted","response_id":"resp-interrupted"}`)},
					{Type: "response.output_item.interrupted", Data: []byte(`{"type":"response.output_item.interrupted","response_id":"resp-interrupted","item_id":"partial","output_index":0}`)},
					{Type: "response.incomplete", Data: []byte(`{"type":"response.incomplete","response":{"id":"resp-interrupted","status":"incomplete","output":[],"incomplete_details":{"reason":"interrupted"},"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}`)},
				} {
					if err := emit(event); err != nil {
						return application.ResponseResult{}, err
					}
				}
				return application.ResponseResult{ResponseID: firstID, Response: []byte(`{"id":"resp-interrupted","status":"incomplete","output":[]}`), Usage: domain.UsageAmount{InputTokens: 3, OutputTokens: 2}, UsageKnown: true, Failed: true}, nil
			}))
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			connection := interruptConnection(t, ctx, server, path)
			interruptWrite(t, ctx, connection, `{"type":"response.create","model":"gpt-6-sol","input":"first"}`)
			interruptExpect(t, ctx, connection, "response.created")
			interruptExpect(t, ctx, connection, "response.output_text.delta")
			interruptWrite(t, ctx, connection, `{"type":"response.interrupt","response_id":"resp-interrupted","mode":"discard_partial_items"}`)
			for _, kind := range []string{"response.interrupt.accepted", "response.output_item.interrupted", "response.incomplete"} {
				interruptExpect(t, ctx, connection, kind)
			}
			totals, err := store.UsageTotals(ctx, "wire-key", "")
			if err != nil || totals.RequestCount != 1 || totals.Usage.InputTokens+totals.Usage.OutputTokens != 5 {
				t.Fatalf("interrupted usage was not settled once: %+v %v", totals, err)
			}
			interruptWrite(t, ctx, connection, `{"type":"response.create","model":"gpt-6-sol","previous_response_id":"resp-interrupted","input":"next"}`)
			interruptExpect(t, ctx, connection, "response.completed")
			if calls.Load() != 2 {
				t.Fatalf("follow-up calls = %d", calls.Load())
			}
			totals, err = store.UsageTotals(ctx, "wire-key", "")
			if err != nil || totals.RequestCount != 2 || totals.Usage.InputTokens+totals.Usage.OutputTokens != 25 {
				t.Fatalf("follow-up usage was not settled once: %+v %v", totals, err)
			}
		})
	}
}

func TestResponseInterruptRejectedControlsPreserveActiveTurn(t *testing.T) {
	var calls atomic.Int32
	server, _ := wireFixture(t, wireProvider(func(ctx context.Context, target application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		calls.Add(1)
		interrupts := make(chan string, 1)
		target.OnInterruptReady(interrupts)
		defer target.OnInterruptReady(nil)
		for _, event := range []application.ResponseEvent{
			{Type: "response.created", Data: []byte(`{"type":"response.created","response":{"id":"owned","status":"in_progress"}}`)},
			{Type: "response.output_text.delta", Data: []byte(`{"type":"response.output_text.delta","delta":"still running"}`)},
		} {
			if err := emit(event); err != nil {
				return application.ResponseResult{}, err
			}
		}
		select {
		case id := <-interrupts:
			if id != "owned" {
				return application.ResponseResult{}, fmt.Errorf("foreign interrupt reached provider: %q", id)
			}
		case <-ctx.Done():
			return application.ResponseResult{}, ctx.Err()
		}
		if err := emit(application.ResponseEvent{Type: "response.interrupt.accepted", Data: []byte(`{"type":"response.interrupt.accepted","response_id":"owned"}`)}); err != nil {
			return application.ResponseResult{}, err
		}
		return wireComplete("owned", emit)
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	connection := interruptConnection(t, ctx, server, "/backend-api/codex/responses")
	interruptWrite(t, ctx, connection, `{"type":"response.create","model":"gpt-6-sol","input":"first"}`)
	interruptExpect(t, ctx, connection, "response.created")
	interruptExpect(t, ctx, connection, "response.output_text.delta")
	interruptWrite(t, ctx, connection, `{"type":"response.interrupt","response_id":"foreign","mode":"discard_partial_items"}`)
	if frame := interruptExpect(t, ctx, connection, "response.interrupt.failed"); !strings.Contains(string(frame), `"response_id":"foreign"`) {
		t.Fatalf("failure did not identify rejected target: %s", frame)
	}
	for _, invalid := range []string{
		`{"type":"response.interrupt","response_id":"owned","mode":"unknown"}`,
		`{"type":"response.interrupt","response_id":"","mode":"discard_partial_items"}`,
		`{"type":"response.interrupt","response_id":42,"mode":"discard_partial_items"}`,
		`{"type":"response.interrupt","response_id":"owned"}`,
		`{"type":"response.interrupt","response_id":"owned","mode":"discard_partial_items","extra":true}`,
		`{"type":"response.interrupt","response_id":"` + strings.Repeat("x", 513) + `","mode":"discard_partial_items"}`,
	} {
		interruptWrite(t, ctx, connection, invalid)
		if frame := interruptExpect(t, ctx, connection, "error"); !strings.Contains(string(frame), "invalid_request") {
			t.Fatalf("invalid control was not rejected: %s", frame)
		}
	}
	interruptWrite(t, ctx, connection, `{"type":"response.interrupt","response_id":"owned","mode":"discard_partial_items"}`)
	interruptExpect(t, ctx, connection, "response.interrupt.accepted")
	interruptExpect(t, ctx, connection, "response.completed")
	interruptWrite(t, ctx, connection, `{"type":"response.interrupt","response_id":"owned","mode":"discard_partial_items"}`)
	if frame := interruptExpect(t, ctx, connection, "response.interrupt.failed"); !strings.Contains(string(frame), `"response_id":"owned"`) {
		t.Fatalf("late failure did not identify target: %s", frame)
	}
	if calls.Load() != 1 {
		t.Fatalf("control dispatched another response: %d", calls.Load())
	}
}

func TestResponseInterruptWithoutReadyProviderDoesNotCancel(t *testing.T) {
	release := make(chan struct{})
	var calls atomic.Int32
	server, _ := wireFixture(t, wireProvider(func(ctx context.Context, _ application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		calls.Add(1)
		for _, event := range []application.ResponseEvent{
			{Type: "response.created", Data: []byte(`{"type":"response.created","response":{"id":"no-ready","status":"in_progress"}}`)},
			{Type: "response.output_text.delta", Data: []byte(`{"type":"response.output_text.delta","delta":"working"}`)},
		} {
			if err := emit(event); err != nil {
				return application.ResponseResult{}, err
			}
		}
		select {
		case <-release:
		case <-ctx.Done():
			return application.ResponseResult{}, ctx.Err()
		}
		return wireComplete("no-ready", emit)
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	connection := interruptConnection(t, ctx, server, "/backend-api/codex/responses")
	interruptWrite(t, ctx, connection, `{"type":"response.create","model":"gpt-6-sol","input":"first"}`)
	interruptExpect(t, ctx, connection, "response.created")
	interruptExpect(t, ctx, connection, "response.output_text.delta")
	interruptWrite(t, ctx, connection, `{"type":"response.interrupt","response_id":"no-ready","mode":"discard_partial_items"}`)
	interruptExpect(t, ctx, connection, "response.interrupt.failed")
	close(release)
	interruptExpect(t, ctx, connection, "response.completed")
	if calls.Load() != 1 {
		t.Fatalf("unsupported interrupt changed dispatch count: %d", calls.Load())
	}
}

func TestResponseInterruptAfterKeyRevocationNeverReachesProvider(t *testing.T) {
	var delivered atomic.Bool
	server, store := wireFixture(t, wireProvider(func(ctx context.Context, target application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		interrupts := make(chan string, 1)
		target.OnInterruptReady(interrupts)
		defer target.OnInterruptReady(nil)
		for _, event := range []application.ResponseEvent{
			{Type: "response.created", Data: []byte(`{"type":"response.created","response":{"id":"revoked","status":"in_progress"}}`)},
			{Type: "response.output_text.delta", Data: []byte(`{"type":"response.output_text.delta","delta":"working"}`)},
		} {
			if err := emit(event); err != nil {
				return application.ResponseResult{}, err
			}
		}
		select {
		case <-interrupts:
			delivered.Store(true)
			return application.ResponseResult{}, errors.New("revoked key sent interrupt")
		case <-ctx.Done():
			return application.ResponseResult{}, ctx.Err()
		}
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	connection := interruptConnection(t, ctx, server, "/backend-api/codex/responses")
	interruptWrite(t, ctx, connection, `{"type":"response.create","model":"gpt-6-sol","input":"first"}`)
	interruptExpect(t, ctx, connection, "response.created")
	interruptExpect(t, ctx, connection, "response.output_text.delta")
	key, err := store.GetAPIKey(ctx, "wire-key")
	if err != nil {
		t.Fatal(err)
	}
	key.IsActive = false
	if err := store.SaveAPIKey(ctx, key, time.Now()); err != nil {
		t.Fatal(err)
	}
	interruptWrite(t, ctx, connection, `{"type":"response.interrupt","response_id":"revoked","mode":"discard_partial_items"}`)
	if frame := interruptExpect(t, ctx, connection, "error"); !strings.Contains(string(frame), "invalid_api_key") {
		t.Fatalf("revoked interrupt was not rejected: %s", frame)
	}
	if delivered.Load() {
		t.Fatal("revoked interrupt reached provider")
	}
}

func TestResponseInterruptCannotReachParallelSocket(t *testing.T) {
	releaseSecond := make(chan struct{}, 1)
	defer close(releaseSecond)
	var foreignDelivered atomic.Bool
	server, _, _ := wireFixtureWithProxyConfig(t, wireProvider(func(ctx context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		var request struct {
			Input string `json:"input"`
		}
		if json.Unmarshal(body, &request) != nil || (request.Input != "first" && request.Input != "second") {
			return application.ResponseResult{}, fmt.Errorf("unexpected branch body: %s", body)
		}
		interrupts := make(chan string, 1)
		target.OnInterruptReady(interrupts)
		defer target.OnInterruptReady(nil)
		id := "response-" + request.Input
		created := fmt.Sprintf(`{"type":"response.created","response":{"id":%q,"status":"in_progress"}}`, id)
		for _, event := range []application.ResponseEvent{
			{Type: "response.created", Data: []byte(created)},
			{Type: "response.output_text.delta", Data: []byte(`{"type":"response.output_text.delta","delta":"working"}`)},
		} {
			if err := emit(event); err != nil {
				return application.ResponseResult{}, err
			}
		}
		if request.Input == "first" {
			select {
			case got := <-interrupts:
				if got != id {
					return application.ResponseResult{}, fmt.Errorf("wrong branch interrupted: %q", got)
				}
			case <-ctx.Done():
				return application.ResponseResult{}, ctx.Err()
			}
			if err := emit(application.ResponseEvent{Type: "response.interrupt.accepted", Data: []byte(`{"type":"response.interrupt.accepted","response_id":"response-first"}`)}); err != nil {
				return application.ResponseResult{}, err
			}
		} else {
			select {
			case <-interrupts:
				foreignDelivered.Store(true)
			case <-releaseSecond:
			case <-ctx.Done():
				return application.ResponseResult{}, ctx.Err()
			}
		}
		return wireComplete(id, emit)
	}), application.ProxyConfig{MaxStreams: 2, MaxQueued: 2, QueueTimeout: time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	first := interruptConnection(t, ctx, server, "/backend-api/codex/responses")
	second := interruptConnection(t, ctx, server, "/backend-api/codex/responses")
	interruptWrite(t, ctx, first, `{"type":"response.create","model":"gpt-6-sol","input":"first"}`)
	interruptWrite(t, ctx, second, `{"type":"response.create","model":"gpt-6-sol","input":"second"}`)
	for _, connection := range []*websocket.Conn{first, second} {
		interruptExpect(t, ctx, connection, "response.created")
		interruptExpect(t, ctx, connection, "response.output_text.delta")
	}
	interruptWrite(t, ctx, first, `{"type":"response.interrupt","response_id":"response-second","mode":"discard_partial_items"}`)
	if frame := interruptExpect(t, ctx, first, "response.interrupt.failed"); !strings.Contains(string(frame), `"response_id":"response-second"`) {
		t.Fatalf("foreign ID failure: %s", frame)
	}
	interruptWrite(t, ctx, first, `{"type":"response.interrupt","response_id":"response-first","mode":"discard_partial_items"}`)
	interruptExpect(t, ctx, first, "response.interrupt.accepted")
	interruptExpect(t, ctx, first, "response.completed")
	releaseSecond <- struct{}{}
	interruptExpect(t, ctx, second, "response.completed")
	if foreignDelivered.Load() {
		t.Fatal("foreign response ID reached its parallel provider")
	}
}

func TestResponseInterruptRelaysThroughProviderToLeasedUpstream(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var sockets, creates, interrupts atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer connection.CloseNow()
		sockets.Add(1)
		for {
			_, body, err := connection.Read(ctx)
			if err != nil {
				return
			}
			var request struct {
				Type     string `json:"type"`
				Previous string `json:"previous_response_id"`
			}
			if json.Unmarshal(body, &request) != nil || request.Type != "response.create" {
				t.Errorf("unexpected upstream create: %s", body)
				return
			}
			switch creates.Add(1) {
			case 1:
				for _, frame := range []string{
					`{"type":"response.created","response":{"id":"upstream-first","status":"in_progress"}}`,
					`{"type":"response.output_text.delta","delta":"partial"}`,
				} {
					if connection.Write(ctx, websocket.MessageText, []byte(frame)) != nil {
						return
					}
				}
				_, body, err = connection.Read(ctx)
				if err != nil {
					return
				}
				var control struct {
					Type       string `json:"type"`
					ResponseID string `json:"response_id"`
					Mode       string `json:"mode"`
				}
				if json.Unmarshal(body, &control) != nil || control.Type != "response.interrupt" || control.ResponseID != "upstream-first" || control.Mode != "discard_partial_items" {
					t.Errorf("wrong upstream interrupt: %s", body)
					return
				}
				interrupts.Add(1)
				for _, frame := range []string{
					`{"type":"response.interrupt.accepted","response_id":"upstream-first"}`,
					`{"type":"response.output_item.interrupted","response_id":"upstream-first","item_id":"partial","output_index":0}`,
					`{"type":"response.incomplete","response":{"id":"upstream-first","status":"incomplete","output":[],"incomplete_details":{"reason":"interrupted"},"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}`,
				} {
					if connection.Write(ctx, websocket.MessageText, []byte(frame)) != nil {
						return
					}
				}
			case 2:
				if request.Previous != "upstream-first" {
					t.Errorf("continuation escaped leased socket: %s", body)
					return
				}
				for _, frame := range []string{
					`{"type":"response.created","response":{"id":"upstream-next","status":"in_progress"}}`,
					`{"type":"response.completed","response":{"id":"upstream-next","status":"completed","output":[],"usage":{"input_tokens":4,"output_tokens":1,"total_tokens":5}}}`,
				} {
					if connection.Write(ctx, websocket.MessageText, []byte(frame)) != nil {
						return
					}
				}
			default:
				t.Errorf("unexpected upstream create count: %d", creates.Load())
				return
			}
		}
	}))
	defer upstream.Close()
	var adapter *provider.Adapter
	server, store := wireFixture(t, wireProvider(func(ctx context.Context, target application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		return adapter.Respond(ctx, target, body, emit)
	}))
	adapter = provider.New(store, fixedTokenSource{}, nil, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL})
	defer adapter.Close()
	connection := interruptConnection(t, ctx, server, "/backend-api/codex/responses")
	interruptWrite(t, ctx, connection, `{"type":"response.create","model":"gpt-6-sol","input":"first"}`)
	interruptExpect(t, ctx, connection, "response.created")
	interruptExpect(t, ctx, connection, "response.output_text.delta")
	interruptWrite(t, ctx, connection, `{"type":"response.interrupt","response_id":"upstream-first","mode":"discard_partial_items"}`)
	for _, kind := range []string{"response.interrupt.accepted", "response.output_item.interrupted", "response.incomplete"} {
		interruptExpect(t, ctx, connection, kind)
	}
	interruptWrite(t, ctx, connection, `{"type":"response.create","model":"gpt-6-sol","previous_response_id":"upstream-first","input":"next"}`)
	interruptExpect(t, ctx, connection, "response.created")
	interruptExpect(t, ctx, connection, "response.completed")
	if sockets.Load() != 1 || creates.Load() != 2 || interrupts.Load() != 1 {
		t.Fatalf("relay changed leased connection: sockets=%d creates=%d interrupts=%d", sockets.Load(), creates.Load(), interrupts.Load())
	}
	totals, err := store.UsageTotals(ctx, "wire-key", "")
	pending, pendingErr := store.ListReservationsNeedingReconciliation(ctx, "", 10)
	if err != nil || pendingErr != nil || totals.RequestCount != 2 || totals.Usage.InputTokens+totals.Usage.OutputTokens != 10 || len(pending) != 0 {
		t.Fatalf("relay accounting: totals=%+v pending=%+v errors=%v/%v", totals, pending, err, pendingErr)
	}
}
