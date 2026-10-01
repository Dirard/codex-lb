package application

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"codex-lb/internal/domain"
	"codex-lb/internal/domain/pricing"
)

type ProxyStore interface {
	Accounts
	AccountQuotaMetadata
	QuotaCandidateAccounts(context.Context, string) ([]domain.Account, error)
	APIKeys
	UsageLedger
	Settings
	Continuations
	CodexResourceOwners
	ScopedOwnerAccounts
	CapabilityLineageStore
	AffinityRoutingStore
}

type ProxyConfig struct {
	MaxStreams         int
	MaxQueued          int
	QueueTimeout       time.Duration
	ResponseTimeout    time.Duration
	ContinuationTTL    time.Duration
	ContinuationBounds domain.ContinuationBounds
}

type ResponseOptions struct {
	KeyID                  string
	Codex                  bool
	SessionID              string
	ThreadID               string
	ClientAffinity         string
	NativeCodexClient      bool
	NativeTransportHint    bool
	CompatibilityMetadata  map[string]string
	TurnState              string
	SynthesizedTurnState   bool
	ConversationID         string
	UserAgent              string
	UserAgentGroup         string
	ClientIP               string
	Transport              string
	CapabilityHeaderValues []string
	ParentTaskIDs          []string
	WindowIDs              []string
	CapabilityRoute        *CapabilityRoute
	LiteState              *ResponsesLiteState
	OnDispatch             func()
}

type Proxy struct {
	OpenResponsePrelude func() (ResponsePrelude, error)
	store               ProxyStore
	provider            ResponseProvider
	cipher              SecretCipher
	config              ProxyConfig
	streams             chan struct{}
	admitted            chan struct{}
	admissionMu         sync.Mutex
	draining            chan struct{}
	nextAccount         atomic.Uint64
	selectionMu         sync.Mutex
	lastSelected        map[string]uint64
	accountAdmission    *accountAdmission
	ResolvePrice        func(context.Context, domain.Account, string) (pricing.Price, error)
	Diagnostics         DiagnosticRecorder
	Catalog             ModelCatalogRoutingPolicy
	Capabilities        *CapabilityRouter
	compact             *CodexOperations
}

func NewProxy(store ProxyStore, provider ResponseProvider, cipher SecretCipher, config ProxyConfig) *Proxy {
	if config.MaxStreams <= 0 {
		config.MaxStreams = 256
	}
	if config.MaxQueued <= 0 {
		config.MaxQueued = 128
	}
	if config.QueueTimeout <= 0 {
		config.QueueTimeout = 15 * time.Second
	}
	if config.ResponseTimeout <= 0 {
		config.ResponseTimeout = 2 * time.Hour
	}
	if config.ContinuationTTL <= 0 {
		config.ContinuationTTL = 24 * time.Hour
	}
	if config.ContinuationBounds.MaxRecords <= 0 {
		config.ContinuationBounds.MaxRecords = 4096
	}
	if config.ContinuationBounds.MaxContextBytes <= 0 {
		config.ContinuationBounds.MaxContextBytes = 128 << 20
	}
	return &Proxy{store: store, provider: provider, cipher: cipher, config: config, streams: make(chan struct{}, config.MaxStreams), admitted: make(chan struct{}, config.MaxStreams+config.MaxQueued), draining: make(chan struct{}), accountAdmission: newAccountAdmission(), Capabilities: NewCapabilityRouter(store), ResolvePrice: func(_ context.Context, account domain.Account, model string) (pricing.Price, error) {
		if account.Kind != domain.AccountChatGPT {
			return pricing.Price{}, pricing.ErrUnpriced
		}
		_, price, err := pricing.LookupCodex(model)
		return price, err
	}}
}

// Acquire shares the server's bounded admission and drain state with ancillary
// Codex operations and operator-enabled synthetic requests.
func (p *Proxy) Acquire(ctx context.Context) (func(), error) { return p.acquire(ctx) }

func (p *Proxy) ConfigureCompaction(operations *CodexOperations) { p.compact = operations }

func (p *Proxy) Respond(ctx context.Context, options ResponseOptions, body json.RawMessage, emit func(ResponseEvent) error) (ResponseResult, error) {
	started := time.Now()
	key, err := p.store.GetAPIKey(ctx, options.KeyID)
	if err != nil || !key.IsActive || key.ExpiresAt != nil && !key.ExpiresAt.After(started) {
		return ResponseResult{}, &ProxyError{Code: "invalid_api_key", Status: 401, Message: "Invalid or expired API key"}
	}
	var route CapabilityRoute
	if options.CapabilityRoute == nil {
		signal, prepared, err := p.PrepareCapability(ctx, options, body)
		if err != nil {
			return ResponseResult{}, err
		}
		body, route = signal.Payload, prepared
	} else {
		// The ingress may prevalidate a raw WebSocket frame, but an internal caller
		// cannot use that route to bypass the real-key and transport boundary.
		if options.KeyID == domain.LocalProxyKeyID && options.CapabilityRoute.RequireSecurityWorkAuthorized {
			return ResponseResult{}, capabilityForbidden("capability_signal_untrusted", "Required capability signal requires an authenticated proxy API key")
		}
		if options.CapabilityRoute.RequireSecurityWorkAuthorized && options.Transport != CapabilityTransportWebSocket {
			return ResponseResult{}, capabilityTransportUnsupported()
		}
		route = *options.CapabilityRoute
	}
	settings, err := p.store.LoadSettings(ctx)
	if err != nil {
		return ResponseResult{}, err
	}
	request, err := parseResponse(body, key, settings, options.Codex)
	if err != nil {
		return ResponseResult{}, err
	}
	if err := prepareResponsesLite(&request, options); err != nil {
		return ResponseResult{}, err
	}
	if request.Stream && emit == nil {
		return ResponseResult{}, errors.New("stream consumer is required")
	}
	if request.CompactionTrigger && request.Stream && options.Transport != CapabilityTransportWebSocket {
		ctx, cancel := context.WithTimeout(ctx, p.config.ResponseTimeout)
		defer cancel()
		return p.compactTriggered(ctx, options, request, emit)
	}
	ctx, cancel := context.WithTimeout(ctx, p.config.ResponseTimeout)
	defer cancel()
	release, err := p.acquire(ctx)
	if err != nil {
		return ResponseResult{}, err
	}
	defer release()
	queueMS := time.Since(started).Milliseconds()
	resolved, history, err := p.resolveOwner(ctx, options, request)
	if err != nil {
		return ResponseResult{}, err
	}
	owner := resolved.Owner
	fileOwner, err := p.resolveFileOwner(ctx, key.ID, request.Object["input"])
	if err != nil {
		return ResponseResult{}, err
	}
	fileOwnerID := fileOwner.AccountID
	if err := resolved.validateFileOwner(fileOwnerID, fileOwner.AccountGeneration, fileOwner.RouteRevision); err != nil {
		return ResponseResult{}, err
	}
	var affinity *requestAffinity
	if owner == nil && fileOwnerID == "" {
		affinity, err = lookupRequestAffinity(ctx, p.store, key.ID, options.identity(), options.ClientAffinity, request, settings)
		if err != nil {
			return ResponseResult{}, err
		}
	}
	if owner != nil && owner.QuotaRefused && (request.FilePinned || owner.FilePinned) {
		return ResponseResult{}, replayUnavailable()
	}
	var pinned domain.Account
	if owner != nil {
		pinned, err = p.store.GetAccount(ctx, owner.AccountID)
		if err != nil || pinned.Generation != owner.AccountGeneration || pinned.RouteRevision != owner.RouteRevision {
			p.retireRequiredSocket(route, owner.AccountID, key.ID)
			return ResponseResult{}, ownerUnavailable()
		}
	}
	var account domain.Account
	var price pricing.Price
	var accountLease *accountLease
	if owner != nil && owner.QuotaRefused {
		request.ResponsesLite = request.ResponsesLite || inputUsesResponsesLite(history.Items)
	}
	if fileOwnerID != "" && owner == nil {
		account, err = p.store.ScopedAccountForOwner(ctx, key.ID, fileOwnerID, time.Now())
		if err != nil || account.Generation != fileOwner.AccountGeneration || account.RouteRevision != fileOwner.RouteRevision {
			p.retireRequiredSocket(route, fileOwnerID, key.ID)
			return ResponseResult{}, ownerUnavailable()
		}
		if _, err := FilterCapabilityAccounts(route, []domain.Account{account}, fileOwnerID); err != nil {
			p.retireRequiredSocket(route, fileOwnerID, key.ID)
			return ResponseResult{}, err
		}
		if _, err := p.catalogAccounts(&request, []domain.Account{account}, account.ID); err != nil {
			p.retireRequiredSocket(route, fileOwnerID, key.ID)
			return ResponseResult{}, err
		}
		price, err = p.ResolvePrice(ctx, account, request.Model)
		if err == nil {
			_, accountLease, err = p.admittedAccount(ctx, []accountCandidate{{account: account, price: price}}, settings, key.ID, true, true)
		}
	} else {
		account, price, accountLease, err = p.selectAccountAdmitted(ctx, key.ID, &request, settings, route, owner, pinned, nil, owner != nil && !owner.QuotaRefused, true, affinity.preferredAccountID())
	}
	if err != nil {
		return ResponseResult{}, err
	}
	defer func() { accountLease.release() }()
	if request.CompactionTrigger && account.Kind != domain.AccountChatGPT {
		return ResponseResult{}, ownerUnavailable()
	}
	request.PreserveReasoning = account.Kind == domain.AccountExternal
	wire := request.Wire
	if !request.WireClean {
		wire, _ = json.Marshal(request.Object)
	}
	if owner != nil && owner.QuotaRefused {
		if err := request.loadInput(); err != nil {
			return ResponseResult{}, err
		}
		wire, err = replayBody(request, history)
		if err != nil || len(owner.ContextEncrypted) == 0 || owner.FilePinned {
			return ResponseResult{}, replayUnavailable()
		}
	}
	excluded := map[string]bool{}
	hydrated := owner != nil && owner.QuotaRefused
	var output *responseOutput
	defer func() { output.close() }()
	for {
		output.close() // A quota replay must discard the rejected owner's prelude.
		if err := ctx.Err(); err != nil {
			return ResponseResult{}, err
		}
		id := "req_" + rand.Text()
		attemptAt := time.Now()
		useWebSocket, allowHTTPFallback := responseTransport(options, request, settings, key, account, wire, hydrated)
		// Select a supported transport before reserving budget: Codex retries
		// this service-level WS error over HTTP with its complete context.
		if options.Transport == CapabilityTransportWebSocket && account.Kind == domain.AccountExternal && !useWebSocket {
			return ResponseResult{}, &ProxyError{Code: "model_source_requires_http_transport", Status: 503, Message: "This model source requires HTTP transport; retry the request over HTTP"}
		}
		budget := domain.UsageAmount{InputTokens: max(1, min(8192, int64(len(wire)/4))), OutputTokens: request.MaxOutput}
		if account.Kind == domain.AccountChatGPT {
			// A rejected wire parameter cannot promise a smaller billed response.
			budget.OutputTokens = max(budget.OutputTokens, defaultOutputTokenEstimate)
		}
		budget.CostMicrodollars, err = price.Cost(budget, request.Tier)
		if err != nil {
			return ResponseResult{}, err
		}
		_, err = p.store.ReserveUsage(ctx, domain.ReservationRequest{ID: id, APIKeyID: key.ID, AccountID: account.ID, AccountGeneration: account.Generation, RouteRevision: account.RouteRevision,
			Continuation: fileOwnerID != "" || owner != nil && account.ID == owner.AccountID,
			Model:        request.Model, Budget: budget, Now: attemptAt})
		if err != nil {
			return ResponseResult{}, err
		}
		// Fresh replay severs inherited trust. Only its retained Lite prefix can
		// enable the new request; no previous owner signal crosses accounts.
		lite := request.ResponsesLite || hydrated && inputUsesResponsesLite(history.Items) || !hydrated && useWebSocket && request.ResponsesLite
		lite = lite && account.Kind == domain.AccountChatGPT
		output = newResponseOutput(responsesLiteOutput(options, request.Model, lite, emit), p.OpenResponsePrelude)
		target := ResponseTarget{Account: account, KeyID: key.ID, UseWebSocket: useWebSocket, AllowHTTPFallback: allowHTTPFallback, RequiredCapability: route.RequireSecurityWorkAuthorized, ResponsesLite: lite}
		if !hydrated {
			target.CompatibilityMetadata = options.CompatibilityMetadata
		}
		if accountLease != nil {
			target.OnFirstUpstreamEvent = accountLease.releaseCreate
		}
		target.SessionID = options.identity().upstreamSessionID(key.ID, account.ID)
		if !hydrated && (fileOwnerID != "" || owner != nil && account.ID == owner.AccountID && !owner.QuotaRefused) {
			target.TurnState = resolved.UpstreamTurnState
		}
		var consumer func(ResponseEvent) error
		var diagnostics DiagnosticEvents
		var deliveryErr error
		marked := map[string]bool{}
		var capabilityErr error
		markResponse := func(ids ...string) error {
			if !route.RequireSecurityWorkAuthorized {
				return nil
			}
			var newIDs []string
			for _, responseID := range ids {
				if responseID != "" && !marked[responseID] {
					newIDs = append(newIDs, responseID)
				}
			}
			if len(newIDs) == 0 {
				return nil
			}
			if err := p.Capabilities.MarkResponseCreated(ctx, key.ID, newIDs...); err != nil {
				capabilityErr = err
				return err
			}
			for _, responseID := range newIDs {
				marked[responseID] = true
			}
			return nil
		}
		if request.Stream {
			consumer = func(event ResponseEvent) error {
				switch event.Type {
				case "response.created", "response.in_progress", "response.completed", "response.failed", "response.incomplete":
					if err := markResponse(responseEventID(event.Data)); err != nil {
						return err
					}
				}
				if p.Diagnostics != nil {
					diagnostics.Add(event)
				}
				deliveryErr = output.send(event)
				return deliveryErr
			}
		}
		var result ResponseResult
		var callErr error
		if err := affinity.save(ctx, p.store, account.ID, id); err != nil {
			callErr = &ProviderFailure{Code: "affinity_persistence_failed", Status: 503}
		} else {
			if options.OnDispatch != nil {
				options.OnDispatch()
			}
			result, callErr = p.dispatch(ctx, target, wire, consumer)
		}
		affinity = nil // Retries use quota/replay rules, never locality hints.
		accountLease.release()
		accountLease = nil
		if deliveryErr != nil {
			callErr = deliveryErr // Adapters must not disguise a local delivery/buffer failure as upstream_error.
		}
		if capabilityErr == nil && callErr == nil {
			if err := markResponse(result.ResponseID); err != nil {
				callErr = err
			}
		}
		if capabilityErr != nil {
			callErr = capabilityErr
		}
		if callErr == nil && result.ResponseID == "" {
			callErr = &ProviderFailure{Code: "invalid_upstream_response", Status: 502, Dispatched: true}
		}
		if callErr == nil && !result.Failed && !result.UsageKnown &&
			!(account.Kind == domain.AccountExternal && result.AllowMissingUsage && !nativeKeyRequiresUsage(key, request.Model)) {
			callErr = &ProviderFailure{Code: "usage_unavailable", Status: 502, Dispatched: true}
		}
		uncharged := !result.UsageKnown && !result.UsageReported || result.UsageKnown && result.Usage.InputTokens == 0 && result.Usage.OutputTokens == 0
		quota := quotaFailure(result, callErr)
		safeZero := !output.visible && !result.OutputObserved && uncharged && (quota || missingPreviousResponse(result, callErr))
		settled, settleErr := p.settle(ctx, id, options, account, request, price, result, callErr, safeZero, started, attemptAt, queueMS, output.firstEventMS, output.firstTokenMS)
		if p.Diagnostics != nil && (callErr != nil || result.Failed || settleErr != nil) && !errors.Is(callErr, context.Canceled) {
			code := result.ErrorCode
			if callErr != nil {
				code = operationErrorCode(callErr)
			}
			if settleErr != nil {
				code = "usage_settlement_failed"
				var failure *ProxyError
				if errors.As(settleErr, &failure) {
					code = failure.Code
				}
			}
			p.Diagnostics(ctx, ErrorDiagnostic{RequestID: id, AccountID: account.ID, AccountGeneration: account.Generation, KeyID: key.ID, Transport: options.Transport, Model: request.Model, Status: "error", ErrorCode: code, OccurredAt: attemptAt, Request: wire, Response: result.Response, Events: diagnostics.Events, EventsTruncated: diagnostics.Truncated})
		}
		if settleErr != nil {
			return result, settleErr
		}
		if capabilityErr != nil {
			p.retireRequiredSocket(route, account.ID, key.ID)
			return result, capabilityErr
		}
		if quota || settled && callErr == nil && !result.Failed {
			cleanup, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			err = p.store.RecordAccountOutcome(cleanup, account.ID, id, quota, !quota)
			if err == nil && quota && owner != nil && owner.AccountID == account.ID {
				err = p.store.MarkContinuationQuotaRefused(cleanup, key.ID, owner.ResponseID, account.ID, id)
			}
			cancelCleanup()
			if err != nil {
				return result, err
			}
		}
		if !settled {
			if output.terminal != nil && callErr == nil {
				if err := output.finish(); err != nil {
					return result, err
				}
			}
			return result, callErr
		}
		if !hydrated && owner != nil && request.Previous != "" && len(owner.ContextEncrypted) > 0 && !owner.FilePinned && safeZero && missingPreviousResponse(result, callErr) {
			replayRequest := request
			replayRequest.ResponsesLite = request.ResponsesLite || inputUsesResponsesLite(history.Items)
			if err := request.loadInput(); err != nil {
				return result, err
			}
			replayRequest.Input = request.Input
			if _, err := p.catalogAccounts(&replayRequest, []domain.Account{account}, account.ID); err != nil {
				return result, err
			}
			if replay, err := replayBody(replayRequest, history); err == nil {
				request = replayRequest
				wire, hydrated = replay, true
				_, accountLease, err = p.admittedAccount(ctx, []accountCandidate{{account: account, price: price}}, settings, key.ID, true, true)
				if err != nil {
					return result, err
				}
				continue // Same account only; a lost cache is not a quota refusal.
			}
		}
		if !quota {
			if result.ResponseID != "" && (callErr == nil || output.visible) {
				if err := request.loadInput(); err != nil {
					return result, err
				}
				if err := p.remember(ctx, options, request, history, owner, account, result, id, target.TurnState != "", callErr == nil && !result.Failed); err != nil {
					return result, err
				}
			}
			if output.terminal != nil && callErr == nil {
				if err := output.finish(); err != nil {
					return result, err
				}
			}
			return result, callErr
		}
		if !safeZero {
			if output.terminal != nil && callErr == nil {
				if err := output.finish(); err != nil {
					return result, err
				}
			}
			return result, callErr
		}
		if request.FilePinned || owner != nil && (owner.FilePinned || len(owner.ContextEncrypted) == 0) {
			if output.terminal != nil && callErr == nil {
				if err := output.finish(); err != nil {
					return result, err
				}
			}
			return result, replayUnavailable()
		}
		hydrated = true
		request.ResponsesLite = request.ResponsesLite || inputUsesResponsesLite(history.Items)
		excluded[account.ID] = true
		// Account scope and candidate state are re-read after each explicit quota
		// refusal. Generic 429/timeouts never enter this branch.
		account, price, accountLease, err = p.selectAccountAdmitted(ctx, key.ID, &request, settings, route, nil, account, excluded, false, true)
		if err != nil {
			return result, err
		}
		if err := request.loadInput(); err != nil {
			return result, err
		}
		wire, err = replayBody(request, history)
		if err != nil {
			return result, replayUnavailable()
		}
	}
}

func missingPreviousResponse(result ResponseResult, err error) bool {
	code := result.ErrorCode
	var failure *ProviderFailure
	if errors.As(err, &failure) {
		code = failure.Code
	}
	return code == "previous_response_not_found" || code == "continuation_not_found"
}

func (p *Proxy) dispatch(ctx context.Context, target ResponseTarget, body json.RawMessage, emit func(ResponseEvent) error) (result ResponseResult, err error) {
	defer func() {
		if recover() != nil {
			result = ResponseResult{}
			err = &ProviderFailure{Code: "upstream_adapter_failure", Status: 502, Dispatched: true}
		}
	}()
	return p.provider.Respond(ctx, target, body, emit)
}

func (p *Proxy) acquire(ctx context.Context) (func(), error) {
	select {
	case <-p.draining:
		return nil, drainingError()
	default:
	}
	select {
	case p.admitted <- struct{}{}:
	default:
		return nil, &ProxyError{Code: "local_capacity_exceeded", Status: 503, Message: "Request queue is full; retry shortly"}
	}
	timer := time.NewTimer(p.config.QueueTimeout)
	defer timer.Stop()
	select {
	case p.streams <- struct{}{}:
		p.admissionMu.Lock()
		defer p.admissionMu.Unlock()
		select {
		case <-p.draining:
			<-p.streams
			<-p.admitted
			return nil, drainingError()
		default:
		}
		return func() { <-p.streams; <-p.admitted }, nil
	case <-p.draining:
		<-p.admitted
		return nil, drainingError()
	case <-ctx.Done():
		<-p.admitted
		return nil, ctx.Err()
	case <-timer.C:
		<-p.admitted
		return nil, &ProxyError{Code: "local_capacity_exceeded", Status: 503, Message: "Timed out waiting for local stream capacity"}
	}
}

// BeginDrain rejects new turns (including turns on an existing WebSocket) and
// releases queued admissions. Already admitted work retains its grace period.
func (p *Proxy) BeginDrain() {
	p.admissionMu.Lock()
	defer p.admissionMu.Unlock()
	select {
	case <-p.draining:
	default:
		close(p.draining)
	}
}

func (p *Proxy) DrainStarted() <-chan struct{} { return p.draining }

func drainingError() error {
	return &ProxyError{Code: "server_draining", Status: 503, Message: "Server is shutting down; retry on the replacement server"}
}

func (p *Proxy) settle(ctx context.Context, id string, options ResponseOptions, account domain.Account, request responseRequest, price pricing.Price, result ResponseResult, callErr error, safeZero bool, started, attemptAt time.Time, queueMS, firstMS, firstTokenMS int64) (bool, error) {
	if operationUsageUncertain(200, result.UsageKnown, callErr) && !safeZero {
		return false, retainUncertainUsage(ctx, p.store, id)
	}
	usage := result.Usage
	if !result.UsageKnown {
		usage = domain.UsageAmount{}
	}
	tier := result.ServiceTier
	if tier == "" {
		tier = request.Tier
	}
	if result.UsageKnown {
		cost, err := price.Cost(usage, tier)
		if err != nil {
			if markErr := retainUncertainUsage(ctx, p.store, id); markErr != nil {
				return false, markErr
			}
			return false, &ProxyError{Code: "invalid_upstream_usage", Status: 502, Message: "Upstream usage could not be accounted; reservation retained"}
		}
		usage.CostMicrodollars = cost
	}
	status, settlement := "success", "finalized"
	code := result.ErrorCode
	if callErr != nil || result.Failed {
		status, settlement = "error", "failed"
		if failure := new(ProviderFailure); errors.As(callErr, &failure) {
			code = failure.Code
		}
		if code == "" {
			code = "stream_incomplete"
		}
		if errors.Is(callErr, context.Canceled) {
			status, code = "cancelled", "request_cancelled"
		} else if errors.Is(callErr, context.DeadlineExceeded) {
			code = "upstream_timeout"
		}
	}
	event := domain.UsageEvent{RequestID: id, APIKeyID: options.KeyID, AccountID: account.ID, AccountGeneration: account.Generation, Model: request.Model, ServiceTier: tier, RequestKind: "normal", Status: status, ErrorCode: code, RequestedAt: attemptAt, QueueLatencyMS: queueMS, FirstEventMS: firstMS, FirstTokenMS: firstTokenMS, TotalLatencyMS: time.Since(started).Milliseconds(), Usage: usage,
		ConversationID: options.ConversationID, UserAgent: options.UserAgent, UserAgentGroup: options.UserAgentGroup,
		ClientIP: options.ClientIP, Transport: options.Transport, ReasoningEffort: request.ReasoningEffort, PlanType: account.PlanType, Source: "openai"}
	if account.Kind == domain.AccountExternal {
		event.ModelSourceID, event.Source = account.ID, "model_source"
	}
	event.ConnectLatencyMS = result.ConnectLatencyMS
	event.ReasoningTokensKnown = result.ReasoningTokensKnown
	if result.TimingsKnown {
		event.FirstEventMS = result.FirstEventMS
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	_, err := p.store.SettleUsage(cleanup, id, domain.UsageSettlement{Status: settlement, Event: event})
	if err != nil {
		return false, &ProxyError{Code: "usage_settlement_failed", Status: 503, Message: "Could not persist request accounting; do not blindly repeat the request"}
	}
	return true, nil
}

func quotaFailure(result ResponseResult, err error) bool {
	var failure *ProviderFailure
	if errors.As(err, &failure) && failure.QuotaRefused {
		return true
	}
	if !result.Failed {
		return false
	}
	switch result.ErrorCode {
	case "insufficient_quota", "usage_limit_reached", "usage_limit_exceeded", "quota_exceeded", "billing_hard_limit_reached":
		return true
	default:
		return false
	}
}

func ownerUnavailable() error {
	return &ProxyError{Code: "previous_response_owner_unavailable", Status: 409, Message: "The previous response owner is no longer allowed for this key"}
}
func replayUnavailable() error {
	return &ProxyError{Code: "quota_failover_requires_context", Status: 409, Message: "Quota was refused but this request cannot safely be replayed on another account"}
}
