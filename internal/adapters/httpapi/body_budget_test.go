package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
	"github.com/coder/websocket"
)

func TestBodyLeaseChargesExactlyAndReleasesOnce(t *testing.T) {
	bodies := NewBodyBudget(100, 0)
	first, second := bodies.lease("a"), bodies.lease("b")
	if got := first.grow(60, true); got != 60 {
		t.Fatalf("first grant = %d", got)
	}
	if got := second.grow(50, true); got != 0 {
		t.Fatalf("all-or-nothing grant exceeded the budget: %d", got)
	}
	if got := second.grow(50, false); got != 40 {
		t.Fatalf("partial grant = %d", got)
	}
	second.refund(10)
	if used := bodies.Usage().UsedBytes; used != 90 {
		t.Fatalf("used after refund = %d", used)
	}
	first.release()
	first.release()
	if used := bodies.Usage().UsedBytes; used != 30 {
		t.Fatalf("double release returned bytes twice: used = %d", used)
	}
	if got := first.grow(10, false); got != 0 {
		t.Fatal("a released lease charged again")
	}
	second.refund(1000)
	second.release()
	if usage := bodies.Usage(); usage.UsedBytes != 0 || usage.LimitBytes != 100 || usage.KeyLimitBytes != 100 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestBodyBudgetPerKeyShareExemptsLocalPrincipal(t *testing.T) {
	bodies := NewBodyBudget(100, 30)
	one := bodies.lease("key-one")
	if one.grow(30, true) != 30 || bodies.lease("key-one").grow(1, false) != 0 {
		t.Fatal("one key exceeded its share")
	}
	if other := bodies.lease("key-two"); other.grow(30, true) != 30 {
		t.Fatal("another key was refused within its share")
	} else {
		other.release()
	}
	local := bodies.lease(domain.LocalProxyKeyID)
	if local.grow(70, true) != 70 {
		t.Fatal("keyless local traffic must be bounded only by the whole budget")
	}
	one.release()
	local.release()
	if used, keys := bodies.Usage().UsedBytes, len(bodies.keys); used != 0 || keys != 0 {
		t.Fatalf("released budget kept used=%d keys=%d", used, keys)
	}
	if whole := NewBodyBudget(100, 100); whole.sharedKey("key-one") {
		t.Fatal("a share equal to the budget must not track keys")
	}
}

func TestChargedReaderAdmitsExactFitAndRefusesOneMoreByte(t *testing.T) {
	bodies := NewBodyBudget(10, 0)
	fit := bodies.lease("key")
	body, err := io.ReadAll(&chargedReader{source: strings.NewReader("0123456789"), lease: fit})
	if err != nil || string(body) != "0123456789" || bodies.Usage().UsedBytes != 10 {
		t.Fatalf("exact fit: %q %v used=%d", body, err, bodies.Usage().UsedBytes)
	}
	fit.release()
	over := bodies.lease("key")
	_, err = io.ReadAll(&chargedReader{source: strings.NewReader("0123456789x"), lease: over})
	if budgetRefusal(err) == nil || bodies.Usage().Rejected != 1 {
		t.Fatalf("one byte over the budget was admitted: %v", err)
	}
	if used := bodies.Usage().UsedBytes; used != 10 {
		t.Fatalf("partial charge = %d, want the bytes read", used)
	}
	over.release()
	prepaid := bodies.lease("key")
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("01234"))
	paid, err := prepaid.admitDeclared(request, 32)
	if err != nil || paid != 5 || bodies.Usage().UsedBytes != 5 {
		t.Fatalf("declared length was not charged up front: %d %v", paid, err)
	}
	body, err = io.ReadAll(&chargedReader{source: request.Body, lease: prepaid, prepaid: paid})
	if err != nil || string(body) != "01234" || bodies.Usage().UsedBytes != 5 {
		t.Fatalf("prepaid body charged twice: %q used=%d", body, bodies.Usage().UsedBytes)
	}
	prepaid.release()
	var tooLarge *application.ProxyError
	if _, err := bodies.lease("key").admitDeclared(httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("x", 33))), 32); !errors.As(err, &tooLarge) || tooLarge.Status != 413 || bodies.Usage().UsedBytes != 0 {
		t.Fatalf("declared length above the maximum: %v", err)
	}
}

func TestBodyBudgetConcurrentLeasesReturnToZero(t *testing.T) {
	bodies := NewBodyBudget(1<<20, 256<<10)
	var wg sync.WaitGroup
	for i := range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lease := bodies.lease([]string{"a", "b", "c", domain.LocalProxyKeyID}[i%4])
			defer lease.release()
			_, _ = io.Copy(io.Discard, &chargedReader{source: bytes.NewReader(make([]byte, 64<<10)), lease: lease})
		}()
	}
	wg.Wait()
	if used, keys := bodies.Usage().UsedBytes, len(bodies.keys); used != 0 || keys != 0 {
		t.Fatalf("concurrent leases leaked used=%d keys=%d", used, keys)
	}
}

// TestWebSocketAndHTTPShareBodyBudget holds the budget with an executing HTTP
// body; a large WebSocket message then gets local capacity on its open socket
// and succeeds once the HTTP request has released its charge.
func TestWebSocketAndHTTPShareBodyBudget(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	server, _ := heartbeatFixture(t, func(ctx context.Context, _ application.ResponseTarget, _ json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		call := calls.Add(1)
		if call == 1 {
			close(entered)
			if err := heartbeatWait(ctx, release); err != nil {
				return application.ResponseResult{}, err
			}
		}
		// Distinct response IDs: the HTTP and WebSocket turns are separate conversations.
		id := fmt.Sprintf("budget-response-%d", call)
		response := json.RawMessage(`{"id":"` + id + `","output":[],"status":"completed","usage":{"input_tokens":1,"output_tokens":1}}`)
		if err := emit(application.ResponseEvent{Type: "response.completed", Data: json.RawMessage(`{"type":"response.completed","response":` + string(response) + `}`)}); err != nil {
			return application.ResponseResult{}, err
		}
		return application.ResponseResult{ResponseID: id, Response: response, Usage: domain.UsageAmount{InputTokens: 1, OutputTokens: 1}, UsageKnown: true}, nil
	})
	p := server.Config.Handler.(*ProxyHandler)
	httpBody := []byte(`{"model":"gpt-6-sol","stream":true,"input":"` + strings.Repeat("y", 8000) + `"}`)
	p.bodies = NewBodyBudget(int64(len(httpBody)+len(largeCreate())-1), 0)
	p.websocketKeepaliveInterval = time.Hour
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	finished := make(chan int, 1)
	go func() {
		request, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/backend-api/codex/responses", bytes.NewReader(httpBody))
		request.Header.Set("Authorization", "Bearer synthetic-heartbeat")
		response, err := server.Client().Do(request)
		if err != nil {
			finished <- 0
			return
		}
		_, _ = io.Copy(io.Discard, response.Body)
		response.Body.Close()
		finished <- response.StatusCode
	}()
	if err := heartbeatWait(ctx, entered); err != nil {
		t.Fatal(err)
	}
	if used := p.bodies.Usage().UsedBytes; used != int64(len(httpBody)) {
		t.Fatalf("executing HTTP body charge = %d, want %d", used, len(httpBody))
	}
	conn := frameConnection(t, ctx, server)
	if err := conn.Write(ctx, websocket.MessageText, largeCreate()); err != nil {
		t.Fatal(err)
	}
	if _, body, err := conn.Read(ctx); err != nil || !strings.Contains(string(body), "local_capacity_exceeded") {
		t.Fatalf("WebSocket message beside a full budget: %s %v", body, err)
	}
	if used := p.bodies.Usage().UsedBytes; used != int64(len(httpBody)) || calls.Load() != 1 {
		t.Fatalf("refused message changed the budget (%d) or dispatched (%d)", used, calls.Load())
	}
	close(release)
	if status := <-finished; status != http.StatusOK {
		t.Fatalf("held HTTP request status = %d", status)
	}
	for deadline := time.Now().Add(time.Second); p.bodies.Usage().UsedBytes != 0; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("finished HTTP request kept its charge")
		}
	}
	if err := conn.Write(ctx, websocket.MessageText, largeCreate()); err != nil {
		t.Fatal(err)
	}
	if _, payload, err := conn.Read(ctx); err != nil || !strings.Contains(string(payload), `"type":"response.completed"`) {
		t.Fatalf("socket unusable after refusal: %s %v", payload, err)
	}
}

func TestNativeChatBodiesAreChargedToTheSharedBudget(t *testing.T) {
	provider := &nativeTestProvider{}
	_, handler := newNativeTestHandler(t, nil, provider, 0)
	bodies := NewBodyBudget(4<<10, 0)
	handler.ConfigureBodyBudget(bodies)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	post := func(content string) *httptest.ResponseRecorder {
		encoded, _ := json.Marshal(map[string]any{"model": "gpt-5.5", "messages": []any{map[string]any{"role": "user", "content": content}}})
		r := httptest.NewRequest(http.MethodPost, "http://localhost/v1/chat/completions", bytes.NewReader(encoded))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer synthetic-native-key")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	if refused := post(strings.Repeat("x", 8<<10)); refused.Code != http.StatusServiceUnavailable || refused.Header().Get("Retry-After") != "2" ||
		!strings.Contains(refused.Body.String(), "local_capacity_exceeded") || len(provider.requests) != 0 {
		t.Fatalf("oversized Chat body: %d %s calls=%d", refused.Code, refused.Body.String(), len(provider.requests))
	}
	if served := post("hello"); served.Code != http.StatusOK || len(provider.requests) != 1 {
		t.Fatalf("fitting Chat body: %d %s", served.Code, served.Body.String())
	}
	if usage := bodies.Usage(); usage.UsedBytes != 0 || usage.Rejected != 1 {
		t.Fatalf("usage = %+v", usage)
	}
}
