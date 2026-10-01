// Package provider routes application Response targets to subscription-backed
// ChatGPT or configured external model sources.
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"codex-lb/internal/adapters/upstream"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

const defaultChatGPTBaseURL = "https://chatgpt.com/backend-api/codex"

var _ application.ResponseProvider = (*Adapter)(nil)

type SourceStore interface {
	application.ModelSourceRepository
	GetAccountCredential(context.Context, string) (domain.AccountCredential, error)
}

// ChatGPTTokenSource is the narrow refresh boundary owned by the ChatGPT
// adapter. It receives ciphertext only and returns a short-lived access token.
type ChatGPTTokenSource interface {
	AccessToken(context.Context, domain.Account, domain.AccountCredential) (string, error)
	ForceRefresh(context.Context, domain.Account, string) (string, error)
}

type Config struct {
	HTTPClient               *http.Client
	ChatGPTBaseURL           string
	CodexVersion             string
	ResolveClientVersion     func(context.Context) (string, error)
	Continuations            *upstream.ContinuationStore
	ChatGPTReasoningFallback func(string) string
}

type Adapter struct {
	store        SourceStore
	cipher       application.SecretCipher
	chatgpt      ChatGPTTokenSource
	client       *upstream.HTTPAdapter
	config       Config
	sourceMu     sync.Mutex
	sourceActive map[string]int
}

func (a *Adapter) Close() error { return a.client.Close() }

func (a *Adapter) RetireRequiredCapability(accountID, keyID string) {
	a.client.RetireRequiredCapability(upstream.Owner{ProviderID: "chatgpt", AccountID: accountID, KeyID: keyID})
}

func New(store SourceStore, tokens ChatGPTTokenSource, cipher application.SecretCipher, config Config) *Adapter {
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{}
	}
	ownedClient := *config.HTTPClient
	ownedClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	config.HTTPClient = &ownedClient
	if config.ChatGPTBaseURL == "" {
		config.ChatGPTBaseURL = defaultChatGPTBaseURL
	}
	if config.CodexVersion == "" {
		config.CodexVersion = domain.DefaultCodexClientVersion
	}
	return &Adapter{
		store: store, cipher: cipher, chatgpt: tokens, client: upstream.New(config.HTTPClient, config.Continuations),
		config: config,
	}
}

func (a *Adapter) Respond(
	ctx context.Context,
	target application.ResponseTarget,
	body json.RawMessage,
	emit func(application.ResponseEvent) error,
) (application.ResponseResult, error) {
	switch target.Account.Kind {
	case domain.AccountChatGPT:
		return a.respondChatGPT(ctx, target, body, emit)
	case domain.AccountExternal:
		return a.respondExternal(ctx, target, body, emit)
	default:
		return application.ResponseResult{}, providerFailure("invalid_account_kind", 0, false)
	}
}

func (a *Adapter) credentialForAccount(ctx context.Context, account domain.Account) (domain.AccountCredential, error) {
	credential, err := a.store.GetAccountCredential(ctx, account.ID)
	if err != nil {
		return domain.AccountCredential{}, err
	}
	if credential.Generation != account.Generation || credential.RouteRevision != account.RouteRevision {
		return domain.AccountCredential{}, domain.ErrConflict
	}
	return credential, nil
}

func (a *Adapter) respondExternal(
	ctx context.Context,
	target application.ResponseTarget,
	body json.RawMessage,
	emit func(application.ResponseEvent) error,
) (application.ResponseResult, error) {
	source, err := a.store.GetModelSource(ctx, target.Account.ID)
	if err != nil {
		return application.ResponseResult{}, providerFailure("model_source_unavailable", 0, false)
	}
	requestModel, stream, err := requestModelAndStream(body)
	if err != nil {
		return application.ResponseResult{}, err
	}
	model, ok := source.Model(requestModel)
	if !ok || !source.Enabled {
		return application.ResponseResult{}, providerFailure("model_source_model_unavailable", 0, false)
	}
	if stream && !model.Streaming {
		return application.ResponseResult{}, providerFailure("streaming_unsupported", 0, false)
	}
	if err := validateReasoningEffort(body, model); err != nil {
		return application.ResponseResult{}, err
	}
	credential, err := a.credentialForAccount(ctx, target.Account)
	if err != nil {
		return application.ResponseResult{}, providerFailure("model_source_credential_unavailable", 0, false)
	}
	key, err := a.plainCredential(credential.ExternalKeyEncrypted)
	if err != nil {
		return application.ResponseResult{}, err
	}

	protocol := upstream.ProtocolChatCompletions
	if source.Responses {
		protocol = upstream.ProtocolResponses
	} else if !source.Chat {
		return application.ResponseResult{}, providerFailure("responses_protocol_unsupported", 0, false)
	}
	transport := upstream.TransportHTTP
	if stream && target.UseWebSocket && protocol == upstream.ProtocolResponses {
		transport = upstream.TransportWebSocket
	}
	capabilities, err := sourceCapabilities(source, model, protocol, transport)
	if err != nil {
		return application.ResponseResult{}, err
	}
	ctx, release, err := a.beginSource(ctx, source)
	if err != nil {
		return application.ResponseResult{}, err
	}
	defer release()
	upstreamTarget := upstream.Target{
		ProviderID: string(source.Kind), AccountID: source.ID, KeyID: target.KeyID,
		AccountGeneration: target.Account.Generation, RouteRevision: target.Account.RouteRevision,
		SessionID: target.SessionID, RequiredCapability: target.RequiredCapability,
		BaseURL: source.BaseURL, Credential: key, Capabilities: capabilities,
	}
	return a.call(ctx, upstreamTarget, body, stream, emit, target.OnFirstUpstreamEvent)
}

func (a *Adapter) respondChatGPT(
	ctx context.Context,
	target application.ResponseTarget,
	body json.RawMessage,
	emit func(application.ResponseEvent) error,
) (application.ResponseResult, error) {
	_, stream, err := requestModelAndStream(body)
	if err != nil {
		return application.ResponseResult{}, err
	}
	credential, err := a.credentialForAccount(ctx, target.Account)
	if err != nil {
		return application.ResponseResult{}, providerFailure("chatgpt_credential_unavailable", 0, false)
	}
	token, err := a.chatgpt.AccessToken(ctx, target.Account, credential)
	if err != nil {
		return application.ResponseResult{}, providerFailure("chatgpt_token_unavailable", 0, false)
	}
	capabilities := upstream.ResponsesCapabilities()
	capabilities.AllowMissingSSEContentType = true
	capabilities.Tools = true
	capabilities.ParallelToolCalls = true
	capabilities.Reasoning = true
	capabilities.ImageInput = true
	capabilities.AllowedHostedTools = []string{
		"web_search", "web_search_preview", "image_generation", "computer", "file_search", "code_interpreter",
	}
	if target.UseWebSocket {
		capabilities.StreamTransport = upstream.TransportWebSocket
	}
	headers, err := a.chatGPTHeaders(ctx)
	if err != nil {
		return application.ResponseResult{}, err
	}
	for _, name := range codexCompatibilityMetadataHeaders {
		if value := target.CompatibilityMetadata[name]; strings.TrimSpace(value) != "" {
			headers.Set(name, value)
		}
	}
	if target.Account.ChatGPTAccountID != "" {
		headers.Set("ChatGPT-Account-ID", target.Account.ChatGPTAccountID)
	}
	if target.SessionID != "" {
		headers.Set("Session_id", target.SessionID)
	}
	if target.TurnState != "" {
		headers.Set("X-Codex-Turn-State", target.TurnState)
	}
	upstreamTarget := upstream.Target{
		ProviderID: "chatgpt", AccountID: target.Account.ID, AccountGeneration: target.Account.Generation, RouteRevision: target.Account.RouteRevision, KeyID: target.KeyID,
		SessionID: target.SessionID, RequiredCapability: target.RequiredCapability, AllowHTTPFallback: target.AllowHTTPFallback,
		BaseURL: a.config.ChatGPTBaseURL, Credential: token, Headers: headers, Capabilities: capabilities,
	}
	result, callErr := a.callChatGPT(ctx, &upstreamTarget, body, stream, target.ResponsesLite, emit, target.OnFirstUpstreamEvent)
	var failure *application.ProviderFailure
	if result.OutputObserved || result.UsageReported && !result.UsageKnown || result.UsageKnown && (result.Usage.InputTokens > 0 || result.Usage.OutputTokens > 0) || !errors.As(callErr, &failure) || failure.Status != http.StatusUnauthorized {
		return result, callErr
	}
	// A definitive authentication rejection is safe to retry once on the same
	// owner before output. Quota/transport failures never trigger token refresh.
	token, err = a.chatgpt.ForceRefresh(ctx, target.Account, token)
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, providerFailure("chatgpt_token_refresh_failed", http.StatusUnauthorized, false)
	}
	upstreamTarget.Credential = token
	return a.callChatGPT(ctx, &upstreamTarget, body, stream, target.ResponsesLite, emit, target.OnFirstUpstreamEvent)
}

func (a *Adapter) call(
	ctx context.Context,
	target upstream.Target,
	body json.RawMessage,
	stream bool,
	emit func(application.ResponseEvent) error,
	firstUpstreamEvent func(),
) (appResult application.ResponseResult, callErr error) {
	var connectStart, connectMS atomic.Int64
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GetConn: func(string) { connectStart.Store(time.Now().UnixNano()) },
		GotConn: func(info httptrace.GotConnInfo) {
			if !info.Reused {
				connectMS.Store(max(1, time.Since(time.Unix(0, connectStart.Load())).Milliseconds()))
			}
		},
	})
	started := time.Now()
	firstEventMS := int64(0)
	defer func() {
		appResult.ConnectLatencyMS = connectMS.Load()
		appResult.FirstEventMS = firstEventMS
		appResult.TimingsKnown = true
	}()
	request := upstream.Request{Body: body}
	var result upstream.Result
	var err error
	observed := false
	var firstOnce sync.Once
	if stream {
		if emit == nil {
			return application.ResponseResult{}, providerFailure("stream_consumer_required", 0, false)
		}
		result, err = a.client.OpenStream(ctx, target, request, func(event upstream.Event) error {
			if firstUpstreamEvent != nil && (target.Capabilities.Protocol != upstream.ProtocolChatCompletions || event.Type != "response.created") {
				firstOnce.Do(firstUpstreamEvent)
			}
			if firstEventMS == 0 && !(target.Capabilities.Protocol == upstream.ProtocolChatCompletions && event.Type == "response.created") {
				firstEventMS = max(1, time.Since(started).Milliseconds())
			}
			if event.Type != "response.created" && event.Type != "response.in_progress" && event.Type != "response.completed" && event.Type != "response.failed" && event.Type != "response.incomplete" && event.Type != "error" {
				observed = true
			}
			return emit(application.ResponseEvent{Type: event.Type, Data: event.Data})
		})
	} else {
		result, err = a.client.Execute(ctx, target, request)
	}
	usage, valid := applicationUsage(result.Usage)
	if !result.UsageKnown || !valid {
		usage = domain.UsageAmount{}
	}
	response := result.Response
	if err != nil || result.Failed {
		response = redactCredential(response, target.Credential)
	}
	appResult = application.ResponseResult{
		ResponseID: result.ResponseID, Response: response,
		Usage: usage, UsageKnown: result.UsageKnown && valid, UsageReported: result.UsageReported,
		AllowMissingUsage: target.Capabilities.AllowMissingUsage, ServiceTier: result.ServiceTier,
		Failed: result.Failed, ErrorCode: result.ErrorCode,
		OutputObserved:       observed,
		ReasoningTokensKnown: result.UsageKnown && valid && result.Usage.ReasoningTokensKnown,
	}
	if result.UsageKnown && !valid {
		return appResult, providerFailure("upstream_usage_overflow", 502, true)
	}
	if err != nil {
		return appResult, wrapUpstreamFailure(err)
	}
	return appResult, nil
}

func (a *Adapter) plainCredential(encrypted []byte) (string, error) {
	if len(encrypted) == 0 {
		return "", nil
	}
	plain, err := a.cipher.Decrypt(encrypted)
	if err != nil {
		return "", providerFailure("credential_decrypt_failed", 0, false)
	}
	return string(plain), nil
}

func requestModelAndStream(body json.RawMessage) (string, bool, error) {
	var request struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.Unmarshal(body, &request); err != nil || strings.TrimSpace(request.Model) == "" {
		return "", false, providerFailure("invalid_response_request", 0, false)
	}
	return request.Model, request.Stream, nil
}

func validateReasoningEffort(body json.RawMessage, model domain.ModelSourceModel) error {
	var request struct {
		Reasoning *struct {
			Effort string `json:"effort"`
		} `json:"reasoning"`
	}
	if json.Unmarshal(body, &request) != nil || request.Reasoning == nil || strings.TrimSpace(request.Reasoning.Effort) == "" {
		return nil
	}
	var metadata modelMetadata
	if strings.TrimSpace(model.RawMetadataJSON) != "" && json.Unmarshal([]byte(model.RawMetadataJSON), &metadata) != nil {
		return providerFailure("invalid_model_metadata", 0, false)
	}
	if !metadata.SupportsReasoning {
		return providerFailure(upstream.ErrorCodeUnsupportedCapability, 0, false)
	}
	supported := metadata.reasoningEfforts()
	if len(supported) == 0 {
		supported = []string{"low", "medium", "high"}
	}
	for _, effort := range supported {
		if upstream.WireReasoningEffort(effort) == upstream.WireReasoningEffort(strings.TrimSpace(request.Reasoning.Effort)) {
			return nil
		}
	}
	return providerFailure("reasoning_effort_unsupported", 0, false)
}

func applicationUsage(usage upstream.Usage) (domain.UsageAmount, bool) {
	if usage.InputTokens > math.MaxInt64 || usage.OutputTokens > math.MaxInt64 ||
		usage.CachedTokens > math.MaxInt64 || usage.ReasoningTokens > math.MaxInt64 {
		return domain.UsageAmount{}, false
	}
	return domain.UsageAmount{
		InputTokens: int64(usage.InputTokens), OutputTokens: int64(usage.OutputTokens),
		CachedInputTokens: int64(usage.CachedTokens),
		ReasoningTokens:   int64(usage.ReasoningTokens),
	}, true
}

func wrapUpstreamFailure(err error) error {
	var upstreamErr *upstream.Error
	classified := errors.As(err, &upstreamErr)
	if (!classified || !upstreamErr.RejectedBeforeExecution) && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		return err
	}
	if !classified {
		return providerFailure("upstream_error", 0, true)
	}
	dispatched := upstreamErr.Status != 0 || upstreamErr.Code != upstream.ErrorCodeInvalidRequest &&
		upstreamErr.Code != upstream.ErrorCodeInvalidConfiguration &&
		upstreamErr.Code != upstream.ErrorCodeUnsupportedCapability && upstreamErr.Code != upstream.ErrorCodeContinuationNotFound
	failure := providerFailure(upstreamErr.Code, upstreamErr.Status, dispatched)
	failure.WebSocketHTTPFallback = upstreamErr.WebSocketHTTPFallback
	failure.RejectedBeforeExecution = upstreamErr.RejectedBeforeExecution
	// Keep cancellation identity without discarding the adapter's proof that
	// this request never reached response.create. Never retain raw error text.
	if errors.Is(err, context.Canceled) {
		return errors.Join(failure, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.Join(failure, context.DeadlineExceeded)
	}
	return failure
}

func providerFailure(code string, status int, dispatched bool) *application.ProviderFailure {
	quota := code == upstream.ErrorCodeInsufficientQuota || code == "usage_limit_reached" ||
		code == "quota_exceeded" || code == "billing_hard_limit_reached"
	return &application.ProviderFailure{Code: code, Status: status, QuotaRefused: quota, Dispatched: dispatched}
}
