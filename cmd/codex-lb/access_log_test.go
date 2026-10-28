package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/adapters/metrics"
	"codex-lb/internal/application"
)

func logLines(t *testing.T, buffer *bytes.Buffer) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buffer.String()), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("log line is not JSON: %q", line)
		}
		lines = append(lines, entry)
	}
	return lines
}

func TestAccessLogRecordsFailuresWithErrorCodeAndRequestID(t *testing.T) {
	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))
	registry := metrics.NewRegistry()
	handler := accessLog(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/responses":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":"no_available_accounts","message":"secret detail sk-live-xyz"}}`))
		default:
			_, _ = w.Write([]byte("ok"))
		}
	}), logger, registry, nil)
	failed := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "http://localhost/v1/responses?token=leak", nil)
	request.RemoteAddr = "203.0.113.9:4000"
	handler.ServeHTTP(failed, request)
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://localhost/dashboard", nil))
	if id := failed.Header().Get("X-Request-Id"); !strings.HasPrefix(id, "req_") || len(id) != 20 {
		t.Fatalf("request id = %q", id)
	}
	lines := logLines(t, &buffer)
	if len(lines) != 1 {
		t.Fatalf("successful request must be logged only at debug: %v", lines)
	}
	entry := lines[0]
	if entry["level"] != "WARN" || entry["status"] != float64(503) || entry["error_code"] != "no_available_accounts" ||
		entry["path"] != "/v1/responses" || entry["group"] != "responses" || entry["client_ip"] != "203.0.113.9" ||
		entry["request_id"] != failed.Header().Get("X-Request-Id") {
		t.Fatalf("entry = %v", entry)
	}
	if text := buffer.String(); strings.Contains(text, "sk-live") || strings.Contains(text, "token=leak") {
		t.Fatalf("log leaked body or query: %s", text)
	}
	var scrape bytes.Buffer
	if err := registry.Write(&scrape); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(scrape.String(), `codex_lb_http_requests_total{code="5xx",group="responses"} 1`) ||
		!strings.Contains(scrape.String(), `codex_lb_http_requests_total{code="2xx",group="dashboard"} 1`) {
		t.Fatalf("metrics = %s", scrape.String())
	}
}

type hijackableRecorder struct{ *httptest.ResponseRecorder }

func (h hijackableRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	server, client := net.Pipe()
	_ = client.Close()
	return server, bufio.NewReadWriter(bufio.NewReader(server), bufio.NewWriter(server)), nil
}

func TestAccessRecorderPreservesHijackAndFlush(t *testing.T) {
	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))
	handler := accessLog(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NewResponseController(w).Flush()
		conn, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Errorf("hijack through access log failed: %v", err)
			return
		}
		_ = conn.Close()
	}), logger, nil, nil)
	handler.ServeHTTP(hijackableRecorder{httptest.NewRecorder()}, httptest.NewRequest("GET", "http://localhost/backend-api/codex/responses", nil))
	lines := logLines(t, &buffer)
	if len(lines) != 1 || lines[0]["websocket"] != true || lines[0]["status"] != float64(101) {
		t.Fatalf("lines = %v", lines)
	}
}

func TestBackgroundReporterDeduplicatesFailuresAndLogsRecovery(t *testing.T) {
	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, nil))
	registry := metrics.NewRegistry()
	reporter := newBackgroundReporter(logger, registry)
	now := time.Unix(1_800_000_000, 0)
	reporter.now = func() time.Time { return now }
	failure := &application.ProviderFailure{Code: "upstream_timeout"}
	reporter.report("account_usage", time.Second, failure)
	reporter.report("account_usage", time.Second, failure)
	now = now.Add(11 * time.Minute)
	reporter.report("account_usage", time.Second, failure)
	reporter.report("account_usage", time.Second, errors.New("database detail with secrets"))
	reporter.report("account_usage", time.Second, nil)
	reporter.report("automations", time.Second, &application.PanicError{Value: "boom", Stack: []byte("goroutine 1")})
	lines := logLines(t, &buffer)
	var messages []string
	for _, line := range lines {
		messages = append(messages, line["msg"].(string)+":"+stringValue(line["error_class"]))
	}
	want := []string{
		"background task failed:provider:upstream_timeout",
		"background task failed:provider:upstream_timeout",
		"background task failed:errors.errorString",
		"background task recovered:",
		"background task panicked:",
	}
	if strings.Join(messages, "|") != strings.Join(want, "|") {
		t.Fatalf("messages = %v", messages)
	}
	if strings.Contains(buffer.String(), "secrets") {
		t.Fatal("error message leaked into the log")
	}
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func TestRouteGroups(t *testing.T) {
	for path, want := range map[string]string{
		"/backend-api/codex/responses": "responses", "/v1/responses/compact": "compact", "/backend-api/codex/responses/compact": "compact",
		"/v1/chat/completions": "chat", "/v1/models": "models", "/backend-api/transcribe": "audio", "/api/accounts": "admin_api",
		"/health/ready": "health", "/": "dashboard", "/v1/usage": "usage", "/v1/realtime/calls": "realtime",
	} {
		if got := routeGroup(path); got != want {
			t.Fatalf("routeGroup(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestRuntimeServesMetricsOnlyOnTheMetricsListener(t *testing.T) {
	cfg := testConfig(t)
	cfg.metricsListen = "127.0.0.1:0"
	var logs bytes.Buffer
	r, err := openRuntime(context.Background(), cfg, slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan struct{})
	r.onServing = func() { close(served) }
	done := make(chan error, 1)
	go func() { done <- r.serveListener(ctx, listener) }()
	<-served
	client := &http.Client{Timeout: 5 * time.Second}
	deadline := time.Now().Add(5 * time.Second)
	for r.metricsAddr() == "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if response, err := client.Get("http://" + listener.Addr().String() + "/metrics"); err != nil || response.StatusCode != http.StatusNotFound {
		t.Fatalf("public listener /metrics: %v %v", response, err)
	} else {
		response.Body.Close()
	}
	response, err := client.Get("http://" + r.metricsAddr() + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	for _, want := range []string{"codex_lb_generations_limit ", `codex_lb_http_requests_total{code="4xx",group="dashboard"} 1`, "codex_lb_database_bytes{file=\"database\"}"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("missing %q in %s", want, body)
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), `"msg":"drain complete"`) || !strings.Contains(logs.String(), `"msg":"metrics listener ready"`) {
		t.Fatalf("lifecycle logs missing: %s", logs.String())
	}
}
