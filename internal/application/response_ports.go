package application

import (
	"context"
	"encoding/json"
	"time"

	"codex-lb/internal/domain"
)

type Continuations interface {
	// Lookup must include KeyID; an ID from another key is indistinguishable from
	// a missing response. Expired records are never returned.
	GetContinuation(context.Context, string, string, time.Time) (domain.Continuation, error)
	SaveContinuation(context.Context, domain.Continuation, domain.ContinuationBounds) error
	SaveSessionContinuation(context.Context, domain.Continuation, string, domain.ContinuationBounds) error
	// The reservation identifies the settled quota refusal; a stale same-owner
	// mark is a no-op and must not touch a newer logical-session generation.
	MarkContinuationQuotaRefused(context.Context, string, string, string, string) error
}

type ResponseTarget struct {
	Account               domain.Account
	KeyID                 string
	UseWebSocket          bool
	AllowHTTPFallback     bool
	RequiredCapability    bool
	ResponsesLite         bool
	CompatibilityMetadata map[string]string
	SessionID             string
	TurnState             string
	OnFirstUpstreamEvent  func()
}

type ResponseEvent struct {
	Type string
	Data json.RawMessage
}

type ResponseResult struct {
	ResponseID           string
	Response             json.RawMessage
	Usage                domain.UsageAmount
	UsageKnown           bool
	UsageReported        bool
	AllowMissingUsage    bool
	ServiceTier          string
	Failed               bool
	ErrorCode            string
	OutputObserved       bool
	ReasoningTokensKnown bool
	ConnectLatencyMS     int64
	FirstEventMS         int64
	TimingsKnown         bool
	DoneMarker           bool
}

type ResponseProvider interface {
	Respond(context.Context, ResponseTarget, json.RawMessage, func(ResponseEvent) error) (ResponseResult, error)
}

// ProviderFailure carries classifications established by the upstream adapter.
// QuotaRefused is only true for an explicit provider quota error, never merely
// HTTP 429, a transport timeout or a local admission error.
type ProviderFailure struct {
	Code                    string
	Status                  int
	QuotaRefused            bool
	Dispatched              bool
	WebSocketHTTPFallback   bool
	RejectedBeforeExecution bool
}

func (e *ProviderFailure) Error() string { return e.Code }
