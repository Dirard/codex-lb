package httpapi

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
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
	bodies                     *BodyBudget
	websockets                 chan struct{}
	mux                        *http.ServeMux
	QuotaHeaders               func(context.Context, string) (map[string]string, error)
	websocketKeepaliveInterval time.Duration
	// websocketPingInterval paces protocol pings during an active downstream
	// WebSocket turn; zero means downstreamPingInterval.
	websocketPingInterval time.Duration
}

// ProxyHandlerLimits sizes ingress budgets; zero values use the 1 GB-host defaults.
// Bodies is the request-body budget shared with the other proxy routes.
type ProxyHandlerLimits struct {
	Bodies           *BodyBudget
	ClientWebSockets int
}

func NewProxyHandler(store ProxyRepository, proxy *application.Proxy, trusted []netip.Prefix) *ProxyHandler {
	return NewProxyHandlerWithLimits(store, proxy, trusted, ProxyHandlerLimits{})
}

func NewProxyHandlerWithLimits(store ProxyRepository, proxy *application.Proxy, trusted []netip.Prefix, limits ProxyHandlerLimits) *ProxyHandler {
	if limits.Bodies == nil {
		limits.Bodies = NewBodyBudget(0, 0)
	}
	if limits.ClientWebSockets <= 0 {
		limits.ClientWebSockets = 4096
	}
	p := &ProxyHandler{store: store, proxy: proxy, trusted: trusted, bodies: limits.Bodies,
		websockets: make(chan struct{}, limits.ClientWebSockets), websocketKeepaliveInterval: 10 * time.Second}
	mux := http.NewServeMux()
	p.RegisterRoutes(mux)
	p.mux = mux
	return p
}

// ClientWebSockets reports admitted client WebSockets and the budget.
func (p *ProxyHandler) ClientWebSockets() (open, limit int) {
	return len(p.websockets), cap(p.websockets)
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
	options.UserAgentGroup = userAgentGroup(options.UserAgent)
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
	// The body stays charged until the response ends: it is retained for
	// failover, compaction and accounting until then.
	lease := p.bodies.lease(key.ID)
	defer lease.release()
	body, err := readProxyBody(w, r, lease)
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

// readProxyBody reads a Responses or Chat body charged to lease as it arrives;
// a gzip body is charged and bounded by its decoded bytes.
func readProxyBody(w http.ResponseWriter, r *http.Request, lease *bodyLease) (json.RawMessage, error) {
	const maximum = domain.MaxRequestBodyBytes
	compressed := false
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding"))) {
	case "", "identity":
	case "gzip":
		compressed = true
	default:
		return nil, &application.ProxyError{Code: "unsupported_content_encoding", Status: 415, Message: "Unsupported request encoding"}
	}
	source, err := lease.open(w, r, maximum)
	if err != nil {
		return nil, err
	}
	if compressed {
		gz, err := gzip.NewReader(source)
		if err != nil {
			if failure := bodyFailure(err); failure != nil {
				return nil, failure
			}
			return nil, &application.ProxyError{Code: "invalid_content_encoding", Status: 400, Message: "Invalid compressed request body"}
		}
		defer gz.Close()
		source = gz
	}
	return readChargedBody(newChargedReader(r, source, lease, maximum, compressed))
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
		return statusClientClosedRequest, "request_cancelled", "Request cancelled"
	default:
		return 500, "proxy_error", "The proxy could not complete this request"
	}
}

func writeProxyError(w http.ResponseWriter, err error) {
	var failure *application.ProviderFailure
	if errors.As(err, &failure) && failure.Code == application.ImageGenerationLimitCode {
		// The subscription's image window, not the account's quota, refused:
		// tell the client in the shape and with the reset headers it expects.
		for name, values := range failure.LimitHeaders {
			w.Header()[name] = append([]string(nil), values...)
		}
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": map[string]string{
			"type": "usage_limit_reached", "code": "usage_limit_reached", "message": "The image generation limit was reached",
		}})
		return
	}
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
