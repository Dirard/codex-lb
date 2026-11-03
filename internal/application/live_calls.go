package application

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"codex-lb/internal/domain"
)

const (
	// liveCallLifetime is how long the subscription keeps a GPT-Live call
	// (session.started reports creation plus two hours); observation ends a
	// grace period after it.
	liveCallLifetime = 2 * time.Hour
	liveCallGrace    = 5 * time.Minute
	// liveStableAttachment resets the reattach backoff.
	liveStableAttachment = 30 * time.Second
)

// liveAttachDelays space the attempts to attach to a call whose media is not
// connected yet; the last one repeats until the attach window closes.
var liveAttachDelays = []time.Duration{100 * time.Millisecond, 300 * time.Millisecond, 800 * time.Millisecond, time.Second}

// CodexLiveCallObserver attaches a read-only sideband to a GPT-Live call so
// the runtime can follow its usage.
type CodexLiveCallObserver interface {
	ObserveLiveCall(ctx context.Context, target CodexOperationTarget, callID string, observe func([]byte)) error
}

// LiveCallConfig bounds GPT-Live observation; zero values take the defaults.
type LiveCallConfig struct {
	// Observers is how many calls one worker meters at once.
	Observers int
	// AttachWindow is how long a new call may take to become attachable.
	AttachWindow time.Duration
	// RetryMin and RetryMax bound the backoff after a dropped sideband.
	RetryMin, RetryMax time.Duration
}

// liveObservers owns the metering sidebands of GPT-Live calls. Each outlives
// the request that created its call, so the runtime closes the group before
// storage.
type liveObservers struct {
	config LiveCallConfig
	slots  chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup
}

// ConfigureLiveCalls enables GPT-Live calls for apps. It must run before the
// first operation and is wired by the composition root.
func (s *CodexOperations) ConfigureLiveCalls(config LiveCallConfig) {
	if config.Observers <= 0 {
		config.Observers = 256
	}
	if config.AttachWindow <= 0 {
		config.AttachWindow = 30 * time.Second
	}
	if config.RetryMin <= 0 {
		config.RetryMin = 200 * time.Millisecond
	}
	if config.RetryMax < config.RetryMin {
		config.RetryMax = max(5*time.Second, config.RetryMin)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.live = &liveObservers{config: config, slots: make(chan struct{}, config.Observers), ctx: ctx, cancel: cancel}
}

// CloseLiveCalls stops observing GPT-Live calls: every observer settles what
// it knows and is joined, and later calls are refused.
func (s *CodexOperations) CloseLiveCalls() {
	if s.live == nil {
		return
	}
	s.live.mu.Lock()
	s.live.closed = true
	s.live.mu.Unlock()
	s.live.cancel()
	s.live.wg.Wait()
}

// acquire takes an observer slot without waiting.
func (o *liveObservers) acquire() (func(), error) {
	o.mu.Lock()
	closed := o.closed
	o.mu.Unlock()
	if closed {
		return nil, &ProxyError{Code: "live_calls_unavailable", Status: 503, Message: "GPT-Live calls are not available"}
	}
	select {
	case o.slots <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-o.slots }) }, nil
	default:
		return nil, &ProxyError{Code: "live_calls_busy", Status: 503, Message: "Too many GPT-Live calls; retry later"}
	}
}

// start runs observe on its own goroutine until the group closes; it
// returns false once the group is closed.
func (o *liveObservers) start(release func(), observe func(context.Context)) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return false
	}
	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		defer release()
		observe(o.ctx)
	}()
	return true
}

// liveCall is a created GPT-Live call and the reservation that pays for it.
type liveCall struct {
	options     CodexOperationOptions
	target      CodexOperationTarget
	callID      string
	model       string
	reservation string
	started     time.Time
	attemptAt   time.Time
}

// liveObservation is what an observer learned about its call.
type liveObservation struct {
	audioMS  int64
	attached bool
	closed   bool
	stopped  bool
}

// CreateAppLiveCall completes an app's GPT-Live session, reserves the call
// budget on a subscription account, creates the call with the quicksilver
// alpha and binds its owner, then leaves an observer to meter the call.
func (s *CodexOperations) CreateAppLiveCall(ctx context.Context, options CodexOperationOptions, credential RealtimeCredential,
	contentType string, body []byte, model string) (CodexOperationResult, error) {
	if len(body) == 0 || len(body) > MaxRealtimeCallBytes {
		return CodexOperationResult{}, &ProxyError{Code: "invalid_request", Status: 400, Message: "Invalid live call request"}
	}
	sdp, session, err := realtimeOffer(contentType, body)
	if err != nil {
		return CodexOperationResult{}, err
	}
	if session == nil {
		session = credential.Session
	}
	session, model, err = liveSession(session, model)
	if err != nil {
		return CodexOperationResult{}, err
	}
	key, err := s.activeKey(ctx, options.KeyID)
	if err != nil {
		return CodexOperationResult{}, err
	}
	observer, ok := s.provider.(CodexLiveCallObserver)
	if !ok || s.live == nil {
		return CodexOperationResult{}, &ProxyError{Code: "live_calls_unavailable", Status: 503, Message: "GPT-Live calls are not available"}
	}
	release, err := s.live.acquire()
	if err != nil {
		return CodexOperationResult{}, err
	}
	observing := false
	defer func() {
		if !observing {
			release()
		}
	}()
	resolved, err := s.resolveConversationOwner(ctx, options)
	if err != nil {
		return CodexOperationResult{}, err
	}
	candidates, err := s.voiceAccounts(ctx, key.ID, resolved.Owner)
	if err != nil {
		return CodexOperationResult{}, err
	}
	offer, _ := json.Marshal(struct {
		SDP     string          `json:"sdp"`
		Session json.RawMessage `json:"session"`
	}{sdp, session})
	var result CodexOperationResult
	for _, account := range candidates[:min(voiceAttempts, len(candidates))] {
		call := liveCall{options: options, model: model, reservation: "req_" + rand.Text(), started: s.now(),
			target: CodexOperationTarget{Account: account, KeyID: key.ID, SessionID: options.upstreamSessionID(account.ID),
				TurnState: resolved.UpstreamTurnState, accountNeutral: true}}
		call.attemptAt = s.now()
		if _, err := s.store.ReserveUsage(ctx, domain.ReservationRequest{
			ID: call.reservation, APIKeyID: key.ID, AccountID: account.ID, AccountGeneration: account.Generation, RouteRevision: account.RouteRevision,
			Model: model, Budget: domain.LiveCallBudget(), Now: call.attemptAt,
		}); err != nil {
			return CodexOperationResult{}, reservationError(err)
		}
		result, err = s.dispatchRealtimeCall(ctx, options, call.target, CodexControlRequest{
			Method: "POST", Path: "realtime/calls", Query: [][2]string{{"intent", "quicksilver"}, {"architecture", "avas"}},
			Body: offer, ContentType: "application/json", RealtimeHeaders: CodexRealtimeHeaders{Alpha: domain.LiveAlpha},
		})
		if err != nil {
			// No call exists, so nothing ran: the reservation goes back unused.
			_ = s.settle(ctx, call.reservation, domain.UsageSettlement{Status: "released"})
			cooldown, refused := voiceQuotaRefusal(err)
			if !refused {
				return result, err
			}
			// A limit refusal created nothing, so the next account may try.
			s.voice.refuse(account.ID, s.now().Add(cooldown))
			continue
		}
		call.callID = realtimeCallID(resultHeaderValue(result.Headers, "Location"))
		counted := s.voice.begin(account.ID)
		observing = s.live.start(release, func(ctx context.Context) {
			defer counted()
			s.observeLiveCall(ctx, observer, call)
		})
		if !observing {
			// The worker began stopping while the call was created.
			counted()
			s.settleLiveCall(ctx, call, liveObservation{stopped: true})
		}
		result.ContentType = "application/sdp"
		result.Headers = map[string][]string{"Location": {"/v1/live/" + call.callID}}
		return result, nil
	}
	return result, err
}

// liveSession completes an app's GPT-Live session: a missing model becomes
// the requested or default Live model and a missing delegation the client
// one, while present fields pass unchanged.
func liveSession(session json.RawMessage, model string) (json.RawMessage, string, error) {
	invalid := &ProxyError{Code: "invalid_request", Status: 400, Message: "session must be an object", Param: "session"}
	fields := map[string]json.RawMessage{}
	if session != nil && (json.Unmarshal(session, &fields) != nil || fields == nil) {
		return nil, "", invalid
	}
	if raw, ok := fields["model"]; ok && string(raw) != "null" {
		if json.Unmarshal(raw, &model) != nil {
			model = ""
		}
	} else {
		if model == "" {
			model = domain.DefaultLiveModel
		}
		fields["model"], _ = json.Marshal(model)
	}
	if !domain.ValidRealtimeModel(model) {
		return nil, "", &ProxyError{Code: "invalid_request", Status: 400, Message: "Invalid live model", Param: "model"}
	}
	if _, ok := fields["delegation"]; !ok {
		fields["delegation"] = json.RawMessage(`{"type":"client"}`)
	}
	completed, _ := json.Marshal(fields)
	return completed, model, nil
}

// observeLiveCall attaches a read-only sideband on the creating account,
// tracks the cumulative audio duration and settles the reservation once at
// the Live price when observation ends.
func (s *CodexOperations) observeLiveCall(ctx context.Context, observer CodexLiveCallObserver, call liveCall) {
	var mu sync.Mutex
	var seen liveObservation
	deadline := call.started.Add(liveCallLifetime + liveCallGrace)
	observe := func(data []byte) {
		var event struct {
			Type    string `json:"type"`
			Session struct {
				ExpiresAt int64 `json:"expires_at"`
			} `json:"session"`
			Usage struct {
				AudioDurationMS int64 `json:"audio_duration_ms"`
			} `json:"usage"`
			UsageLimit struct {
				Status       json.RawMessage `json:"status"`
				ResetSeconds json.RawMessage `json:"reset_seconds"`
			} `json:"usage_limit"`
		}
		if json.Unmarshal(data, &event) != nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch event.Type {
		case "session.started":
			if event.Session.ExpiresAt > 0 {
				deadline = time.Unix(event.Session.ExpiresAt, 0).Add(liveCallGrace)
			}
		case "session.usage.updated", "session.closed":
			seen.audioMS = max(seen.audioMS, event.Usage.AudioDurationMS)
			seen.closed = seen.closed || event.Type == "session.closed"
			if status := event.UsageLimit.Status; len(status) != 0 && string(status) != "null" {
				// The subscription's voice limit is near: new calls go elsewhere.
				var reset float64
				_ = json.Unmarshal(event.UsageLimit.ResetSeconds, &reset)
				s.voice.warn(call.target.Account.ID, s.now().Add(voiceCooldown(reset)))
			}
		}
	}
	config := s.live.config
	attachUntil := s.now().Add(config.AttachWindow)
	attempts, backoff := 0, config.RetryMin
	attached := false
	for {
		mu.Lock()
		until := deadline
		mu.Unlock()
		attemptCtx, cancel := context.WithDeadline(ctx, until)
		began := s.now()
		err := observer.ObserveLiveCall(attemptCtx, call.target, call.callID, observe)
		cancel()
		mu.Lock()
		closed := seen.closed
		mu.Unlock()
		var failure *ProviderFailure
		refused := errors.As(err, &failure) && failure.Code == "live_observer_unavailable"
		if err == nil || errors.As(err, &failure) && failure.Code == "live_observer_disconnected" {
			attached = true
		}
		now := s.now()
		if closed || ctx.Err() != nil || !now.Before(until) {
			break
		}
		var delay time.Duration
		switch {
		case !attached:
			// Media may still be connecting; only the attach window waits.
			if now.Before(attachUntil) {
				delay = liveAttachDelays[min(attempts, len(liveAttachDelays)-1)]
				attempts++
			}
		case refused && (failure.Status == 404 || failure.Status == 410):
			// The call is over and its sideband with it.
		default:
			if now.Sub(began) >= liveStableAttachment {
				backoff = config.RetryMin
			}
			delay, backoff = backoff, min(2*backoff, config.RetryMax)
		}
		if delay == 0 {
			break
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
		case <-timer.C:
		}
		timer.Stop()
	}
	mu.Lock()
	observation := seen
	mu.Unlock()
	observation.attached = attached
	observation.stopped = ctx.Err() != nil && !observation.closed
	s.settleLiveCall(ctx, call, observation)
}

// settleLiveCall charges a call's known audio duration once at the Live
// price.
func (s *CodexOperations) settleLiveCall(ctx context.Context, call liveCall, observation liveObservation) {
	status, code := "success", ""
	switch {
	case observation.closed:
	case observation.stopped:
		status, code = "cancelled", "live_usage_incomplete"
	case !observation.attached:
		status, code = "error", "live_call_unobserved"
	default:
		status, code = "error", "live_usage_incomplete"
	}
	usage, err := domain.LiveUsage(observation.audioMS)
	if err != nil {
		usage, code = domain.UsageAmount{}, "invalid_upstream_usage"
		if status == "success" {
			status = "error"
		}
	}
	options := call.options
	options.Transport = "webrtc"
	event := s.usageEvent(options, call.target.Account, call.model, "live_session", status, CodexOperationResult{ErrorCode: code}, usage,
		call.reservation, call.started, call.attemptAt)
	settlement := "finalized"
	if status != "success" {
		settlement = "failed"
	}
	// A failed write leaves the reservation to reconciliation.
	_ = s.settle(ctx, call.reservation, domain.UsageSettlement{Status: settlement, Event: event})
}
