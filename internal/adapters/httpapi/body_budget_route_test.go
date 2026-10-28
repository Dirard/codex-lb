package httpapi_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/adapters/httpapi"
	"codex-lb/internal/adapters/provider"
	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

type budgetReply struct {
	status int
	code   string
	retry  string
}

func postBudgetBody(ctx context.Context, url, token, contentType string, body io.Reader, headers ...string) (budgetReply, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return budgetReply{}, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", contentType)
	for i := 0; i+1 < len(headers); i += 2 {
		request.Header.Set(headers[i], headers[i+1])
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return budgetReply{}, err
	}
	defer response.Body.Close()
	payload, _ := io.ReadAll(response.Body)
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(payload, &envelope)
	return budgetReply{status: response.StatusCode, code: envelope.Error.Code, retry: response.Header.Get("Retry-After")}, nil
}

// undeclared hides a reader's length so the client sends a chunked body.
type undeclared struct{ io.Reader }

func responsesBody(marker string, size int) []byte {
	prefix := `{"model":"gpt-6-sol","input":"` + marker
	return []byte(prefix + strings.Repeat("x", max(0, size-len(prefix)-2)) + `"}`)
}

func waitBudget(t *testing.T, bodies *httpapi.BodyBudget, want int64) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); bodies.Usage().UsedBytes != want; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("retained body bytes = %d, want %d", bodies.Usage().UsedBytes, want)
		}
	}
}

func raiseKeyLimit(t *testing.T, store *sqlite.Store, id string) {
	t.Helper()
	key, err := store.GetAPIKey(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	key.Limits[0].MaxValue = 1 << 40
	if err := store.SaveAPIKey(context.Background(), key, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func budgetResponsesServer(t *testing.T, provider wireProvider, bodies *httpapi.BodyBudget) (*httptest.Server, *sqlite.Store) {
	t.Helper()
	_, store, proxy := wireFixtureWithProxyConfig(t, provider, application.ProxyConfig{MaxStreams: 8, MaxQueued: 8, QueueTimeout: time.Second})
	raiseKeyLimit(t, store, "wire-key")
	server := httptest.NewServer(httpapi.NewProxyHandlerWithLimits(store, proxy, nil, httpapi.ProxyHandlerLimits{Bodies: bodies}))
	t.Cleanup(server.Close)
	return server, store
}

// TestCompactBurstBeyondBodyBudgetGetsLocalCapacity is the NET-14 regression:
// compact bodies were read with no bound at all.
func TestCompactBurstBeyondBodyBudgetGetsLocalCapacity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, store, proxy := wireFixtureWithProxyConfig(t, nil, application.ProxyConfig{MaxStreams: 8, MaxQueued: 8, QueueTimeout: time.Second})
	raiseKeyLimit(t, store, "wire-key")
	var calls atomic.Int64
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"compact","object":"response.compact","output":[{"type":"compaction","encrypted_content":"opaque"}],"usage":{"input_tokens":3,"output_tokens":2}}`)
	}))
	t.Cleanup(upstream.Close)
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	adapter := provider.New(store, fixedTokenSource{}, nil, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL + "/codex"})
	t.Cleanup(func() { adapter.Close() })
	operations := application.NewCodexOperations(store, store, adapter, time.Hour)
	operations.ConfigureAdmission(proxy)
	operations.ConfigureAccountSelection(proxy)
	body := responsesBody("compact", 48<<10)
	bodies := httpapi.NewBodyBudget(int64(3*len(body)), 0)
	handler := httpapi.NewCodexOperationsHandler(store, operations, nil)
	handler.ConfigureBodyBudget(bodies)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	post := func() (budgetReply, error) {
		return postBudgetBody(ctx, server.URL+"/backend-api/codex/responses/compact", "synthetic-key", "application/json", bytes.NewReader(body))
	}
	admitted := make(chan budgetReply, 3)
	for range 3 {
		go func() {
			reply, _ := post()
			admitted <- reply
		}()
	}
	for deadline := time.Now().Add(2 * time.Second); calls.Load() != 3; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("admitted compacts reached upstream: %d", calls.Load())
		}
	}
	waitBudget(t, bodies, int64(3*len(body)))
	for range 2 {
		reply, err := post()
		if err != nil || reply.status != http.StatusServiceUnavailable || reply.code != "local_capacity_exceeded" || reply.retry != "2" {
			t.Fatalf("excess compact: %+v %v", reply, err)
		}
	}
	if calls.Load() != 3 || bodies.Usage().Rejected != 2 {
		t.Fatalf("refused compacts reached upstream: calls=%d usage=%+v", calls.Load(), bodies.Usage())
	}
	close(release)
	for range 3 {
		if reply := <-admitted; reply.status != http.StatusOK {
			t.Fatalf("admitted compact: %+v", reply)
		}
	}
	waitBudget(t, bodies, 0)
	account, err := store.GetAccount(ctx, "wire-account")
	if err != nil || account.Status != domain.AccountActive {
		t.Fatalf("local body pressure changed account health: %+v %v", account.Status, err)
	}
}

// TestResponsesBodyBudgetPartialFailuresReleaseExactlyOnce runs an upstream
// failure, a client cancellation and a validation failure beside an executing
// request; each releases its own charge once and the executing one keeps its own.
func TestResponsesBodyBudgetPartialFailuresReleaseExactlyOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	entered := map[string]chan struct{}{"hold": make(chan struct{}), "cancel": make(chan struct{})}
	release := make(chan struct{})
	bodies := httpapi.NewBodyBudget(1<<20, 0)
	server, _ := budgetResponsesServer(t, func(ctx context.Context, _ application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		switch {
		case bytes.Contains(body, []byte(`"input":"hold`)):
			close(entered["hold"])
			select {
			case <-release:
			case <-ctx.Done():
				return application.ResponseResult{}, ctx.Err()
			}
			return wireComplete("held", emit)
		case bytes.Contains(body, []byte(`"input":"cancel`)):
			close(entered["cancel"])
			<-ctx.Done()
			return application.ResponseResult{}, ctx.Err()
		default:
			return application.ResponseResult{}, &application.ProviderFailure{Code: "server_error", Status: 500, RejectedBeforeExecution: true}
		}
	}, bodies)
	url := server.URL + "/v1/responses"
	post := func(ctx context.Context, body []byte) (budgetReply, error) {
		return postBudgetBody(ctx, url, "synthetic-key", "application/json", bytes.NewReader(body))
	}
	hold := responsesBody("hold", 100<<10)
	held := make(chan budgetReply, 1)
	go func() {
		reply, _ := post(ctx, hold)
		held <- reply
	}()
	<-entered["hold"]
	waitBudget(t, bodies, int64(len(hold)))

	if reply, err := post(ctx, responsesBody("fail", 50<<10)); err != nil || reply.status < 500 || reply.code == "local_capacity_exceeded" {
		t.Fatalf("upstream failure: %+v %v", reply, err)
	}
	waitBudget(t, bodies, int64(len(hold)))

	cancelled, stop := context.WithCancel(ctx)
	cancelBody := responsesBody("cancel", 70<<10)
	gone := make(chan error, 1)
	go func() {
		_, err := post(cancelled, cancelBody)
		gone <- err
	}()
	<-entered["cancel"]
	waitBudget(t, bodies, int64(len(hold)+len(cancelBody)))
	stop()
	if err := <-gone; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled client: %v", err)
	}
	waitBudget(t, bodies, int64(len(hold)))

	invalid := []byte(`{"model":"gpt-6-sol","input":"` + strings.Repeat("x", 30<<10))
	if reply, err := post(ctx, invalid); err != nil || reply.status != http.StatusBadRequest {
		t.Fatalf("validation failure: %+v %v", reply, err)
	}
	waitBudget(t, bodies, int64(len(hold)))

	close(release)
	if reply := <-held; reply.status != http.StatusOK {
		t.Fatalf("executing request: %+v", reply)
	}
	waitBudget(t, bodies, 0)
	if usage := bodies.Usage(); usage.Rejected != 0 {
		t.Fatalf("failures were reported as body pressure: %+v", usage)
	}
}

func TestResponsesUndeclaredAndGzipBodiesAreChargedWhileRead(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var calls atomic.Int64
	held, release := make(chan struct{}), make(chan struct{})
	bodies := httpapi.NewBodyBudget(64<<10, 0)
	server, _ := budgetResponsesServer(t, func(_ context.Context, _ application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		if calls.Add(1) == 1 {
			close(held)
			<-release
		}
		return wireComplete(fmt.Sprintf("charged_%d", calls.Load()), emit)
	}, bodies)
	url := server.URL + "/v1/responses"
	small := responsesBody("chunked", 10<<10)
	done := make(chan budgetReply, 1)
	go func() {
		reply, _ := postBudgetBody(ctx, url, "synthetic-key", "application/json", undeclared{bytes.NewReader(small)})
		done <- reply
	}()
	<-held
	// An undeclared body is charged exactly, byte by byte, while it executes.
	waitBudget(t, bodies, int64(len(small)))
	close(release)
	if reply := <-done; reply.status != http.StatusOK {
		t.Fatalf("chunked body within the budget: %+v", reply)
	}
	waitBudget(t, bodies, 0)
	compress := func(raw []byte) []byte {
		var out bytes.Buffer
		writer := gzip.NewWriter(&out)
		_, _ = writer.Write(raw)
		_ = writer.Close()
		return out.Bytes()
	}
	for name, send := range map[string]func([]byte) (budgetReply, error){
		"chunked": func(raw []byte) (budgetReply, error) {
			return postBudgetBody(ctx, url, "synthetic-key", "application/json", undeclared{bytes.NewReader(raw)})
		},
		"gzip": func(raw []byte) (budgetReply, error) {
			return postBudgetBody(ctx, url, "synthetic-key", "application/json", bytes.NewReader(compress(raw)), "Content-Encoding", "gzip")
		},
	} {
		before := calls.Load()
		if reply, err := send(responsesBody(name, 100<<10)); err != nil || reply.status != http.StatusServiceUnavailable || reply.code != "local_capacity_exceeded" {
			t.Fatalf("%s body beyond the budget: %+v %v", name, reply, err)
		}
		if calls.Load() != before || bodies.Usage().UsedBytes != 0 {
			t.Fatalf("%s refusal dispatched (%d) or kept its partial charge (%d)", name, calls.Load()-before, bodies.Usage().UsedBytes)
		}
		if reply, err := send(responsesBody(name, 20<<10)); err != nil || reply.status != http.StatusOK {
			t.Fatalf("%s body within the budget: %+v %v", name, reply, err)
		}
	}
	if usage := bodies.Usage(); usage.UsedBytes != 0 || usage.Rejected != 2 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestResponsesBodyBudgetKeyShare(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	held, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	bodies := httpapi.NewBodyBudget(200<<10, 100<<10)
	server, store := budgetResponsesServer(t, func(_ context.Context, _ application.ResponseTarget, body json.RawMessage, emit func(application.ResponseEvent) error) (application.ResponseResult, error) {
		if calls.Add(1) == 1 {
			close(held)
			<-release
		}
		return wireComplete(fmt.Sprintf("share_%d", calls.Load()), emit)
	}, bodies)
	if err := store.SaveAPIKey(ctx, domain.APIKey{ID: "second-key", Name: "second", KeyHash: fmt.Sprintf("%x", sha256.Sum256([]byte("synthetic-second-key"))), KeyPrefix: "synthetic", IsActive: true}, time.Now()); err != nil {
		t.Fatal(err)
	}
	url := server.URL + "/v1/responses"
	done := make(chan budgetReply, 1)
	first := responsesBody("first", 80<<10)
	go func() {
		reply, _ := postBudgetBody(ctx, url, "synthetic-key", "application/json", bytes.NewReader(first))
		done <- reply
	}()
	<-held
	waitBudget(t, bodies, int64(len(first)))
	if reply, err := postBudgetBody(ctx, url, "synthetic-key", "application/json", bytes.NewReader(responsesBody("again", 40<<10))); err != nil ||
		reply.status != http.StatusServiceUnavailable || reply.code != "local_capacity_exceeded" {
		t.Fatalf("key beyond its share: %+v %v", reply, err)
	}
	if reply, err := postBudgetBody(ctx, url, "synthetic-second-key", "application/json", bytes.NewReader(responsesBody("other", 40<<10))); err != nil || reply.status != http.StatusOK {
		t.Fatalf("another key within the budget: %+v %v", reply, err)
	}
	close(release)
	if reply := <-done; reply.status != http.StatusOK {
		t.Fatalf("held request: %+v", reply)
	}
	waitBudget(t, bodies, 0)
	if calls.Load() != 2 {
		t.Fatalf("refused body reached the provider: %d calls", calls.Load())
	}
}

func TestTranscriptionBodiesAreChargedToTheSharedBudget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"text":"ok"}`)
	}))
	t.Cleanup(upstream.Close)
	_, store, proxy := wireFixtureWithProxy(t, nil)
	raiseKeyLimit(t, store, "wire-key")
	adapter := provider.New(store, fixedTokenSource{}, nil, provider.Config{HTTPClient: upstream.Client(), ChatGPTBaseURL: upstream.URL + "/codex"})
	t.Cleanup(func() { adapter.Close() })
	operations := application.NewCodexOperations(store, store, adapter, time.Hour)
	operations.ConfigureAdmission(proxy)
	bodies := httpapi.NewBodyBudget(32<<10, 0)
	handler := httpapi.NewCodexOperationsHandler(store, operations, nil)
	handler.ConfigureBodyBudget(bodies)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	post := func(audio int, chunked bool) (budgetReply, error) {
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		_ = form.WriteField("model", "gpt-4o-transcribe")
		file, _ := form.CreateFormFile("file", "synthetic.wav")
		_, _ = file.Write(bytes.Repeat([]byte("a"), audio))
		_ = form.Close()
		var reader io.Reader = &body
		if chunked {
			reader = undeclared{&body}
		}
		return postBudgetBody(ctx, server.URL+"/backend-api/transcribe", "synthetic-key", form.FormDataContentType(), reader)
	}
	for _, chunked := range []bool{false, true} {
		if reply, err := post(64<<10, chunked); err != nil || reply.status != http.StatusServiceUnavailable || reply.code != "local_capacity_exceeded" {
			t.Fatalf("audio beyond the budget (chunked=%v): %+v %v", chunked, reply, err)
		}
		if reply, err := post(1<<10, chunked); err != nil || reply.status != http.StatusOK {
			t.Fatalf("audio within the budget (chunked=%v): %+v %v", chunked, reply, err)
		}
	}
	if usage := bodies.Usage(); usage.UsedBytes != 0 || usage.Rejected != 2 || calls.Load() != 2 {
		t.Fatalf("usage = %+v calls = %d", usage, calls.Load())
	}
}
