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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"codex-lb/internal/adapters/antigravity"
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

// GrokTokenSource lends a Grok pool account's current access token to a
// Grok subscription source. rejectedVersion, when not negative, asks for a
// token newer than the one xAI rejected.
type GrokTokenSource interface {
	GrokAccessToken(ctx context.Context, accountID string, rejectedVersion int64) (string, int64, error)
}

// AntigravityTokens lends an Antigravity source its account's current
// access token and Cloud Code project, and records refusals that change the
// account. rejectedVersion, when not negative, asks for a token newer than
// the one Google rejected.
type AntigravityTokens interface {
	AntigravityAccessToken(ctx context.Context, sourceID string, rejectedVersion int64) (token string, version int64, project string, err error)
	// ReportAntigravityRefusal records an account refusal: code is
	// upstream.ErrorCodeAccountSuspended or
	// upstream.ErrorCodeAccountValidationRequired, actionURL its link.
	ReportAntigravityRefusal(ctx context.Context, sourceID, code, actionURL string) error
}

type Config struct {
	HTTPClient               *http.Client
	ChatGPTBaseURL           string
	CodexVersion             string
	ResolveClientVersion     func(context.Context) (string, error)
	Continuations            *upstream.ContinuationStore
	ChatGPTReasoningFallback func(string) string
	// UpstreamWebSocketSessions bounds live ChatGPT WebSocket sessions; 0 keeps the default.
	UpstreamWebSocketSessions int
	// GrokTokens serves Grok subscription sources; without it they fail
	// before dispatch.
	GrokTokens GrokTokenSource
	// GrokBaseURL is where Grok subscription tokens go, whatever a source
	// stores; tests replace it.
	GrokBaseURL string
	// AntigravityTokens serves Antigravity sources; without it they fail
	// before dispatch.
	AntigravityTokens AntigravityTokens
	// AntigravityBaseURL is where Antigravity tokens go (domain
	// AntigravityBaseURL); tests replace it.
	AntigravityBaseURL string
	// ResolveAntigravityClientVersion reads the Antigravity client version of
	// the Cloud Code User-Agent (the runtime setting) on every call; nil
	// means the default version.
	ResolveAntigravityClientVersion func(context.Context) (string, error)
}

type Adapter struct {
	store        SourceStore
	cipher       application.SecretCipher
	chatgpt      ChatGPTTokenSource
	client       *upstream.HTTPAdapter
	config       Config
	sourceMu     sync.Mutex
	sourceActive map[string]int
	realtime     realtimeBounds
	// sourceCall sends a model-source request upstream; nil means a.call.
	// Tests replace it to observe the target a source dispatch builds.
	sourceCall func(ctx context.Context, target upstream.Target, body json.RawMessage, stream bool,
		emit func(application.ResponseEvent) error, firstUpstreamEvent func()) (application.ResponseResult, error)
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
	if config.GrokBaseURL == "" {
		config.GrokBaseURL = domain.GrokTextBaseURL
	}
	if config.AntigravityBaseURL == "" {
		config.AntigravityBaseURL = domain.AntigravityBaseURL
	}
	client := upstream.New(config.HTTPClient, config.Continuations)
	client.SetWebSocketSessionLimit(config.UpstreamWebSocketSessions)
	return &Adapter{
		store: store, cipher: cipher, chatgpt: tokens, client: client,
		config: config, realtime: defaultRealtimeBounds,
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
	grok := source.Kind == domain.ModelSourceGrokSubscription
	antigravitySource := source.Kind == domain.ModelSourceAntigravity
	// A Grok Responses request is reshaped to what xAI accepts, which
	// includes fitting its reasoning effort to the model.
	var names grokToolNames
	if grok && source.Responses {
		if body, names, err = adaptGrokRequest(body, upstreamModelName(model)); err != nil {
			return application.ResponseResult{}, err
		}
	} else if err := validateReasoningEffort(body, model); err != nil {
		return application.ResponseResult{}, err
	}
	credential, err := a.credentialForAccount(ctx, target.Account)
	if err != nil {
		return application.ResponseResult{}, providerFailure("model_source_credential_unavailable", 0, false)
	}
	var key, project string
	var tokenVersion int64
	baseURL := source.BaseURL
	switch {
	case grok:
		key, tokenVersion, err = a.grokToken(ctx, source, -1)
		baseURL = a.config.GrokBaseURL
	case antigravitySource:
		// The stored credential is the refresh token; only the service that
		// owns it reads it, and the provider takes an access token.
		key, tokenVersion, project, err = a.antigravityToken(ctx, source, -1)
		baseURL = a.config.AntigravityBaseURL
	default:
		key, err = a.plainCredential(credential.ExternalKeyEncrypted)
	}
	if err != nil {
		return application.ResponseResult{}, err
	}

	protocol := upstream.ProtocolChatCompletions
	switch {
	case antigravitySource:
		protocol = upstream.ProtocolAntigravity
	case source.Responses:
		protocol = upstream.ProtocolResponses
	case !source.Chat:
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
		BaseURL: baseURL, Credential: key, Capabilities: capabilities,
	}
	if antigravitySource {
		version := antigravity.DefaultClientVersion
		if a.config.ResolveAntigravityClientVersion != nil {
			if version, err = a.config.ResolveAntigravityClientVersion(ctx); err != nil || !antigravity.ValidClientVersion(version) {
				return application.ResponseResult{}, providerFailure("antigravity_client_version_unavailable", 0, false)
			}
		}
		upstreamTarget.AntigravityProject = project
		upstreamTarget.AntigravityUserAgent = antigravity.UserAgent(version)
	}
	if len(names) != 0 && emit != nil {
		clientEmit := emit
		emit = func(event application.ResponseEvent) error {
			event.Data = names.restoreEvent(event.Type, event.Data)
			return clientEmit(event)
		}
	}
	send := a.sourceCall
	if send == nil {
		send = a.call
	}
	result, callErr := send(ctx, upstreamTarget, body, stream, emit, target.OnFirstUpstreamEvent)
	if (grok || antigravitySource) && retryableAuthRejection(result, callErr) {
		// The subscription token expired or was revoked before the provider
		// did anything: the same account may answer once more with a newer
		// token.
		if grok {
			upstreamTarget.Credential, _, err = a.grokToken(ctx, source, tokenVersion)
		} else {
			upstreamTarget.Credential, _, upstreamTarget.AntigravityProject, err = a.antigravityToken(ctx, source, tokenVersion)
		}
		if err != nil {
			return result, err
		}
		result, callErr = send(ctx, upstreamTarget, body, stream, emit, target.OnFirstUpstreamEvent)
	}
	if antigravitySource {
		a.applyAntigravityRefusal(ctx, source.ID, result, callErr)
	}
	result.Response = names.restoreResponse(result.Response)
	return result, callErr
}

// antigravityToken takes an Antigravity source's current access token and
// Cloud Code project. A source that needs a new sign-in, was suspended or is
// gone fails before dispatch.
func (a *Adapter) antigravityToken(ctx context.Context, source domain.ModelSource, rejectedVersion int64) (string, int64, string, error) {
	if a.config.AntigravityTokens == nil {
		return "", 0, "", providerFailure("antigravity_account_unavailable", 0, false)
	}
	token, version, project, err := a.config.AntigravityTokens.AntigravityAccessToken(ctx, source.ID, rejectedVersion)
	if err != nil {
		if ctx.Err() != nil {
			return "", 0, "", ctx.Err()
		}
		return "", 0, "", providerFailure("antigravity_account_unavailable", 0, false)
	}
	return token, version, project, nil
}

// applyAntigravityRefusal hands a suspension or verification request to the
// token service, which changes the account; the request itself stays refused
// and is never moved to another account.
func (a *Adapter) applyAntigravityRefusal(ctx context.Context, sourceID string, result application.ResponseResult, callErr error) {
	if a.config.AntigravityTokens == nil {
		return
	}
	code, actionURL := result.ErrorCode, ""
	var refusal *accountRefusal
	var failure *application.ProviderFailure
	if errors.As(callErr, &refusal) {
		code, actionURL = refusal.Code, refusal.actionURL
	} else if errors.As(callErr, &failure) {
		code = failure.Code
	}
	if code != upstream.ErrorCodeAccountSuspended && code != upstream.ErrorCodeAccountValidationRequired {
		return
	}
	// The service records the effect with its own detached deadline.
	_ = a.config.AntigravityTokens.ReportAntigravityRefusal(ctx, sourceID, code, actionURL)
}

// accountRefusal is a provider failure that changes the account behind it.
// Its link (an appeal or verification URL) stays inside the provider and
// never reaches the client.
type accountRefusal struct {
	*application.ProviderFailure
	actionURL string
}

func (e *accountRefusal) Unwrap() error { return e.ProviderFailure }

// upstreamModelName is the provider's own name for a source model.
func upstreamModelName(model domain.ModelSourceModel) string {
	if target := strings.TrimSpace(model.UpstreamModel); target != "" {
		return target
	}
	return model.Model
}

// grokToken takes the Grok account's current token for a subscription source.
func (a *Adapter) grokToken(ctx context.Context, source domain.ModelSource, rejectedVersion int64) (string, int64, error) {
	if a.config.GrokTokens == nil {
		return "", 0, providerFailure("grok_account_unavailable", 0, false)
	}
	token, version, err := a.config.GrokTokens.GrokAccessToken(ctx, source.ProviderConfig.GrokAccountID, rejectedVersion)
	if err != nil {
		if ctx.Err() != nil {
			return "", 0, ctx.Err()
		}
		return "", 0, providerFailure("grok_account_unavailable", 0, false)
	}
	return token, version, nil
}

// retryableAuthRejection is a definitive 401 that left no trace for the
// client: no output, usage or failed terminal event.
func retryableAuthRejection(result application.ResponseResult, err error) bool {
	var failure *application.ProviderFailure
	return !result.OutputObserved && !result.UsageReported && !result.UsageKnown &&
		errors.As(err, &failure) && failure.Status == http.StatusUnauthorized
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
		OnInterruptReady: target.OnInterruptReady,
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
			synthetic := syntheticStreamEvent(target.Capabilities.Protocol, event.Type)
			if firstUpstreamEvent != nil && !synthetic {
				firstOnce.Do(firstUpstreamEvent)
			}
			if firstEventMS == 0 && !synthetic {
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

// syntheticStreamEvent is the response.created a translated stream (Chat
// Completions, Antigravity) emits before the provider answered anything; it
// neither releases the create lease nor counts as the first upstream event.
func syntheticStreamEvent(protocol upstream.Protocol, eventType string) bool {
	return eventType == "response.created" && (protocol == upstream.ProtocolChatCompletions || protocol == upstream.ProtocolAntigravity)
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
	failure.LimitHeaders = upstreamErr.LimitHeaders
	if upstreamErr.RetryAfter > 0 && len(failure.LimitHeaders["Retry-After"]) == 0 {
		// The provider's own hint (Google's retry delay) tells the client
		// when to come back, in whole seconds of at most a day.
		headers := make(map[string][]string, len(failure.LimitHeaders)+1)
		for name, values := range failure.LimitHeaders {
			headers[name] = values
		}
		seconds := min(int64(math.Ceil(upstreamErr.RetryAfter.Seconds())), 86400)
		headers["Retry-After"] = []string{strconv.FormatInt(seconds, 10)}
		failure.LimitHeaders = headers
	}
	if upstreamErr.ActionURL != "" && (upstreamErr.Code == upstream.ErrorCodeAccountSuspended || upstreamErr.Code == upstream.ErrorCodeAccountValidationRequired) {
		return &accountRefusal{ProviderFailure: failure, actionURL: upstreamErr.ActionURL}
	}
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
