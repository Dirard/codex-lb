package httpapi

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
	"codex-lb/internal/domain/pricing"
)

type ProxyRepository interface {
	application.APIKeys
	application.Settings
}

type ProxyHandler struct {
	store                      ProxyRepository
	proxy                      *application.Proxy
	trusted                    []netip.Prefix
	reading                    chan struct{}
	websockets                 chan struct{}
	websocketBodies            chan struct{}
	mux                        *http.ServeMux
	QuotaHeaders               func(context.Context, string) (map[string]string, error)
	websocketKeepaliveInterval time.Duration
}

func NewProxyHandler(store ProxyRepository, proxy *application.Proxy, trusted []netip.Prefix) *ProxyHandler {
	p := &ProxyHandler{store: store, proxy: proxy, trusted: trusted, reading: make(chan struct{}, 128),
		websockets:      make(chan struct{}, 4096),
		websocketBodies: make(chan struct{}, 2*proxy.AdmissionCapacity()), websocketKeepaliveInterval: 10 * time.Second}
	mux := http.NewServeMux()
	p.RegisterRoutes(mux)
	p.mux = mux
	return p
}

// RegisterRoutes keeps exact Responses paths ahead of ancillary call-ID wildcards
// when the handler is composed into the runtime's shared proxy mux.
func (p *ProxyHandler) RegisterRoutes(mux *http.ServeMux) {
	for _, path := range []string{"/v1/responses", "/v1/responses/{$}", "/backend-api/codex/responses", "/backend-api/codex/responses/{$}"} {
		mux.HandleFunc("POST "+path, p.responses)
		mux.HandleFunc("GET "+path, p.websocket)
	}
}

func (p *ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) { p.mux.ServeHTTP(w, r) }

func (p *ProxyHandler) writeQuotaHeaders(w http.ResponseWriter, r *http.Request, key domain.APIKey) bool {
	if p.QuotaHeaders == nil || key.ID == domain.LocalProxyKeyID {
		return true
	}
	headers, err := p.QuotaHeaders(r.Context(), key.ID)
	if err != nil {
		writeProxyError(w, err)
		return false
	}
	for name, value := range headers {
		w.Header().Set(name, value)
	}
	return true
}

func (p *ProxyHandler) authenticate(r *http.Request) (domain.APIKey, error) {
	return authenticateProxyKey(r, p.store)
}

func invalidKey() error {
	return &application.ProxyError{Code: "invalid_api_key", Status: 401, Message: "A valid proxy API key is required"}
}

func responseOptions(r *http.Request, keyID string) application.ResponseOptions {
	metadata := make(map[string]string)
	for _, name := range []string{"x-codex-turn-metadata", "x-openai-subagent", "x-codex-parent-thread-id", "x-codex-window-id"} {
		if value := r.Header.Get(name); strings.TrimSpace(value) != "" {
			metadata[name] = value
		}
	}
	return application.ResponseOptions{KeyID: keyID, Codex: strings.HasPrefix(r.URL.Path, "/backend-api/"),
		CompatibilityMetadata: metadata,
		NativeCodexClient:     application.NativeCodexClient(r.UserAgent(), r.Header.Get("Originator")),
		NativeTransportHint:   conversationHeader(r, "X-Codex-Turn-State", "X-Codex-Turn-Metadata", "X-Codex-Beta-Features") != "",
		SessionID:             conversationHeader(r, "Session_id", "Session-Id", "X-Codex-Session-Id", "X-Codex-Conversation-Id"),
		ThreadID:              conversationHeader(r, "Thread-Id"), TurnState: conversationHeader(r, "X-Codex-Turn-State"),
		ClientAffinity:         conversationHeader(r, "X-Parent-Session-Id", "X-Opencode-Session", "X-Session-Id", "X-Session-Affinity"),
		CapabilityHeaderValues: r.Header.Values(domain.RequiredCapabilityHeader), ParentTaskIDs: r.Header.Values("X-Codex-Parent-Thread-Id"), WindowIDs: r.Header.Values("X-Codex-Window-Id")}
}

func conversationHeader(r *http.Request, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(r.Header.Get(name)); value != "" {
			return value
		}
	}
	return ""
}

func (p *ProxyHandler) responseOptions(r *http.Request, keyID string) application.ResponseOptions {
	options := responseOptions(r, keyID)
	options.Transport = "http"
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		options.Transport = "websocket"
	}
	identity := ResolveIdentity(r, p.trusted)
	if identity.IP.IsValid() {
		options.ClientIP = identity.IP.String()
	}
	options.UserAgent = strings.TrimSpace(r.UserAgent())
	if len(options.UserAgent) > 1024 {
		options.UserAgent = options.UserAgent[:1024]
	}
	if fields := strings.Fields(options.UserAgent); len(fields) > 0 {
		options.UserAgentGroup, _, _ = strings.Cut(fields[0], "/")
	}
	headers := []string{}
	switch {
	case strings.HasPrefix(strings.ToLower(options.UserAgent), "codex"):
		headers = []string{"Thread-Id"}
	case strings.HasPrefix(strings.ToLower(options.UserAgent), "opencode"):
		headers = []string{"X-Parent-Session-Id", "X-Opencode-Session", "X-Session-Id", "X-Session-Affinity"}
	}
	for _, header := range headers {
		if value := strings.TrimSpace(r.Header.Get(header)); value != "" && len(value) <= 512 {
			options.ConversationID = value
			break
		}
	}
	return options
}

func (p *ProxyHandler) responses(w http.ResponseWriter, r *http.Request) {
	key, err := p.authenticate(r)
	if err != nil {
		writeProxyError(w, err)
		return
	}
	if !p.writeQuotaHeaders(w, r, key) {
		return
	}
	select {
	case p.reading <- struct{}{}:
	default:
		writeProxyError(w, &application.ProxyError{Code: "local_capacity_exceeded", Status: 503, Message: "Too many incoming request bodies"})
		return
	}
	body, err := readProxyBody(w, r)
	<-p.reading
	if err != nil {
		writeProxyError(w, err)
		return
	}
	options := p.responseOptions(r, key.ID)
	signal, route, err := p.proxy.PrepareCapability(r.Context(), options, body)
	if err != nil {
		writeProxyError(w, err)
		return
	}
	body = signal.Payload
	options.CapabilityRoute = &route
	var shape struct {
		Stream bool `json:"stream"`
	}
	if json.Unmarshal(body, &shape) != nil {
		writeProxyError(w, &application.ProxyError{Code: "invalid_request", Status: 400, Message: "Invalid Responses request"})
		return
	}
	if shape.Stream {
		p.stream(w, r, options, body)
		return
	}
	result, err := p.proxy.Respond(r.Context(), options, body, nil)
	if err != nil {
		writeProxyError(w, err)
		return
	}
	if !json.Valid(result.Response) {
		writeProxyError(w, &application.ProxyError{Code: "invalid_upstream_response", Status: 502, Message: "Invalid upstream response"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result.Response)
}

func readProxyBody(w http.ResponseWriter, r *http.Request) (json.RawMessage, error) {
	const maximum = 32 << 20
	var reader io.Reader = http.MaxBytesReader(w, r.Body, maximum)
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding"))) {
	case "", "identity":
	case "gzip":
		gz, err := gzip.NewReader(reader)
		if err != nil {
			return nil, &application.ProxyError{Code: "invalid_content_encoding", Status: 400, Message: "Invalid compressed request body"}
		}
		defer gz.Close()
		reader = gz
	default:
		return nil, &application.ProxyError{Code: "unsupported_content_encoding", Status: 415, Message: "Unsupported request encoding"}
	}
	body, err := io.ReadAll(io.LimitReader(reader, maximum+1))
	var tooLarge *http.MaxBytesError
	if len(body) > maximum || errors.As(err, &tooLarge) {
		return nil, &application.ProxyError{Code: "request_too_large", Status: 413, Message: "Request body exceeds size limit"}
	}
	if err != nil {
		return nil, &application.ProxyError{Code: "invalid_request_body", Status: 400, Message: "Could not decode request body"}
	}
	return body, nil
}

func proxyError(err error) (int, string, string) {
	var proxy *application.ProxyError
	if errors.As(err, &proxy) {
		return proxy.Status, proxy.Code, proxy.Message
	}
	var upstream *application.ProviderFailure
	if errors.As(err, &upstream) {
		status := upstream.Status
		if status < 400 || status > 599 {
			status = 502
		}
		return status, upstream.Code, "Upstream request failed"
	}
	switch {
	case errors.Is(err, domain.ErrLimitReached):
		return 429, "api_key_limit_exceeded", "This API key's configured limit has been reached"
	case errors.Is(err, domain.ErrNoAccounts):
		return 503, "no_available_accounts", "No permitted account is currently available"
	case errors.Is(err, pricing.ErrUnpriced):
		return 400, "model_not_priced", "No price is configured for this model and provider"
	case errors.Is(err, context.DeadlineExceeded):
		return 504, "upstream_timeout", "The response deadline was exceeded"
	case errors.Is(err, context.Canceled):
		return 499, "request_cancelled", "Request cancelled"
	default:
		return 500, "proxy_error", "The proxy could not complete this request"
	}
}

func writeProxyError(w http.ResponseWriter, err error) {
	status, code, message := proxyError(err)
	if status == 429 && (code == "account_response_create_cap" || code == "account_stream_cap" || code == "api_key_stream_fair_share") {
		w.Header().Set("Retry-After", "2")
		writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "type": "rate_limit_error", "message": message}})
		return
	}
	if status == 503 {
		w.Header().Set("Retry-After", "2")
	}
	var request *application.ProxyError
	if errors.As(err, &request) && request.Param != "" {
		writeJSON(w, status, map[string]any{"error": map[string]any{
			"code": code, "type": "invalid_request_error", "message": message, "param": request.Param,
		}})
		return
	}
	writeError(w, status, code, message)
}
