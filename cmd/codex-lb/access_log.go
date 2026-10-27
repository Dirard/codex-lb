package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"codex-lb/internal/adapters/httpapi"
	"codex-lb/internal/adapters/metrics"
)

// maxErrorBodyCapture bounds how much of an error response is inspected for
// the stable error code; bodies are never logged.
const maxErrorBodyCapture = 2048

// accessLog assigns every request an ID, returns it in X-Request-Id and logs
// one line when the request (or WebSocket) ends: failures at INFO/WARN,
// successes at DEBUG. Only the method, path without query, status, size,
// duration, resolved client IP and our own error code are logged.
func accessLog(next http.Handler, logger *slog.Logger, registry *metrics.Registry, trusted []netip.Prefix) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		requestID := "req_" + strings.ToLower(rand.Text()[:16])
		w.Header().Set("X-Request-Id", requestID)
		group := routeGroup(r.URL.Path)
		var finish func(string, string, time.Duration)
		if registry != nil {
			finish = registry.RequestStarted()
		}
		recorder := &accessRecorder{ResponseWriter: w}
		defer func() {
			elapsed := time.Since(started)
			status := recorder.status
			if status == 0 {
				status = http.StatusOK
			}
			if finish != nil {
				finish(group, statusClass(status), elapsed)
			}
			level := slog.LevelDebug
			switch {
			case status >= 500:
				level = slog.LevelWarn
			case status >= 400:
				level = slog.LevelInfo
			}
			if group == "health" {
				level = slog.LevelDebug
			}
			if !logger.Enabled(r.Context(), level) {
				return
			}
			attrs := []slog.Attr{
				slog.String("request_id", requestID), slog.String("method", r.Method), slog.String("path", r.URL.Path),
				slog.String("group", group), slog.Int("status", status), slog.Int64("bytes", recorder.bytes),
				slog.Int64("duration_ms", elapsed.Milliseconds()),
			}
			if ip := httpapi.ResolveIdentity(r, trusted).IP; ip.IsValid() {
				attrs = append(attrs, slog.String("client_ip", ip.String()))
			}
			if recorder.hijacked {
				attrs = append(attrs, slog.Bool("websocket", true))
			}
			if code := errorCode(recorder.errorBody); code != "" {
				attrs = append(attrs, slog.String("error_code", code))
			}
			logger.LogAttrs(context.WithoutCancel(r.Context()), level, "http request", attrs...)
		}()
		next.ServeHTTP(recorder, r)
	})
}

type accessRecorder struct {
	http.ResponseWriter
	status    int
	bytes     int64
	errorBody []byte
	hijacked  bool
}

func (w *accessRecorder) WriteHeader(status int) {
	if w.status == 0 && (status >= 200 || status == http.StatusSwitchingProtocols) {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *accessRecorder) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.status >= 400 && len(w.errorBody) < maxErrorBodyCapture {
		w.errorBody = append(w.errorBody, data[:min(len(data), maxErrorBodyCapture-len(w.errorBody))]...)
	}
	n, err := w.ResponseWriter.Write(data)
	w.bytes += int64(n)
	return n, err
}

func (w *accessRecorder) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *accessRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("response writer does not support hijacking")
	}
	conn, buffer, err := hijacker.Hijack()
	if err == nil {
		w.hijacked = true
		if w.status == 0 {
			w.status = http.StatusSwitchingProtocols
		}
	}
	return conn, buffer, err
}

// Unwrap lets http.ResponseController reach deadlines and flushing.
func (w *accessRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// errorCode extracts our stable error.code from a JSON error envelope.
func errorCode(body []byte) string {
	if len(body) == 0 || body[0] != '{' {
		return ""
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	if decoder.Decode(&envelope) != nil {
		return ""
	}
	code := envelope.Error.Code
	if len(code) > 64 {
		return ""
	}
	for _, char := range code {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '.' || char == '-') {
			return ""
		}
	}
	return code
}

func statusClass(status int) string { return strconv.Itoa(status/100) + "xx" }

// routeGroup maps a path to a fixed, low-cardinality group for logs and metrics.
func routeGroup(path string) string {
	switch {
	case strings.HasPrefix(path, "/health"):
		return "health"
	case strings.Contains(path, "/compact"):
		return "compact"
	case strings.HasPrefix(path, "/v1/responses"), strings.HasPrefix(path, "/backend-api/codex/responses"):
		return "responses"
	case strings.HasPrefix(path, "/v1/chat/"):
		return "chat"
	case strings.HasPrefix(path, "/v1/embeddings"):
		return "embeddings"
	case strings.Contains(path, "/realtime"):
		return "realtime"
	case strings.HasPrefix(path, "/v1/audio/"), strings.Contains(path, "/transcribe"):
		return "audio"
	case strings.Contains(path, "/files"):
		return "files"
	case strings.HasPrefix(path, "/v1/models"), strings.HasPrefix(path, "/backend-api/codex/models"):
		return "models"
	case strings.HasPrefix(path, "/v1/usage"), strings.HasPrefix(path, "/api/codex/usage"):
		return "usage"
	case strings.HasPrefix(path, "/v1/"), strings.HasPrefix(path, "/backend-api/"), strings.HasPrefix(path, "/api/codex/"):
		return "proxy_other"
	case strings.HasPrefix(path, "/api/"):
		return "admin_api"
	default:
		return "dashboard"
	}
}
