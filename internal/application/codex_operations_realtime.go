package application

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/url"
	"strings"

	"codex-lb/internal/domain"
)

func (s *CodexOperations) CreateRealtimeCall(ctx context.Context, options CodexOperationOptions, request CodexControlRequest) (CodexOperationResult, error) {
	if _, err := s.activeKey(ctx, options.KeyID); err != nil {
		return CodexOperationResult{}, err
	}
	mediaType, params, err := mime.ParseMediaType(request.ContentType)
	if err != nil || mediaType != "application/json" && mediaType != "application/sdp" && mediaType != "multipart/form-data" || mediaType == "multipart/form-data" && params["boundary"] == "" {
		return CodexOperationResult{}, &ProxyError{Code: "invalid_content_type", Status: 415, Message: "Unsupported realtime call content type"}
	}
	if len(request.Body) == 0 || len(request.Body) > MaxRealtimeCallBytes || mediaType == "application/json" && !json.Valid(request.Body) {
		return CodexOperationResult{}, &ProxyError{Code: "invalid_request", Status: 400, Message: "Invalid realtime call request"}
	}
	if err := validateControlQuery(request.Query); err != nil {
		return CodexOperationResult{}, err
	}
	if err := request.RealtimeHeaders.Validate(); err != nil {
		return CodexOperationResult{}, err
	}
	request.Method, request.Path = "POST", "realtime/calls"
	resolved, err := s.resolveConversationOwner(ctx, options)
	if err != nil {
		return CodexOperationResult{}, err
	}
	account, err := s.accountForOwner(ctx, options.KeyID, resolved.Owner, nil)
	if err != nil {
		return CodexOperationResult{}, err
	}
	target := CodexOperationTarget{Account: account, KeyID: options.KeyID,
		SessionID: options.upstreamSessionID(account.ID), TurnState: resolved.UpstreamTurnState}
	return s.contentFree(ctx, options, account, "realtime-call", "realtime_call", nil, target, func() (CodexOperationResult, error) {
		result, err := s.provider.Control(ctx, target, request)
		if err != nil || result.Failed || result.Status < 200 || result.Status >= 300 {
			if ctx.Err() != nil {
				return CodexOperationResult{}, ctx.Err()
			}
			status := result.Status
			var failure *ProviderFailure
			if errors.As(err, &failure) {
				status = failure.Status
			}
			if status < 400 || status > 599 {
				status = 502
			}
			return CodexOperationResult{}, &ProviderFailure{Code: "realtime_call_unavailable", Status: status, Dispatched: true}
		}
		callID := realtimeCallID(resultHeaderValue(result.Headers, "Location"))
		if callID == "" {
			return CodexOperationResult{}, &ProviderFailure{Code: "realtime_call_binding_failed", Status: 503, Dispatched: true}
		}
		ownerRecord := CodexResourceOwner{
			ResourceType: CodexResourceRealtime, ResourceID: callID, KeyID: options.KeyID,
			AccountID: account.ID, AccountGeneration: account.Generation, RouteRevision: account.RouteRevision, ExpiresAt: s.now().Add(s.ttl),
		}
		if err := s.owners.SaveCodexResourceOwner(ctx, ownerRecord); err != nil {
			return CodexOperationResult{}, &ProviderFailure{Code: "realtime_call_binding_failed", Status: 503, Dispatched: true}
		}
		return result, nil
	})
}

func (s *CodexOperations) AuthorizeRealtime(ctx context.Context, options CodexOperationOptions, callID string) (CodexOperationTarget, error) {
	if _, err := s.activeKey(ctx, options.KeyID); err != nil {
		return CodexOperationTarget{}, err
	}
	if !validRealtimeCallID(callID) {
		return CodexOperationTarget{}, &ProxyError{Code: "invalid_realtime_call_id", Status: 400, Message: "Invalid realtime call ID"}
	}
	owner, err := s.owners.GetCodexResourceOwner(ctx, CodexResourceRealtime, callID, options.KeyID, s.now())
	if errors.Is(err, domain.ErrNotFound) {
		return CodexOperationTarget{}, &ProxyError{Code: "realtime_call_owner_not_found", Status: 409, Message: "Realtime call owner is unavailable"}
	}
	if err != nil {
		return CodexOperationTarget{}, err
	}
	resolved, err := resolveConversation(ctx, s.store, options.KeyID, "", options.identity(), s.now())
	if err != nil {
		return CodexOperationTarget{}, err
	}
	if err := resolved.validateFileOwner(owner.AccountID, owner.AccountGeneration, owner.RouteRevision); err != nil {
		return CodexOperationTarget{}, err
	}
	account, err := s.strictOwnerAccount(ctx, options.KeyID, owner.AccountID, owner.AccountGeneration, owner.RouteRevision)
	if err != nil {
		return CodexOperationTarget{}, err
	}
	return CodexOperationTarget{Account: account, KeyID: options.KeyID, SessionID: options.upstreamSessionID(account.ID)}, nil
}

func ValidateCodexRealtimeRequest(request CodexRealtimeRequest) error {
	if !validRealtimeCallID(request.CallID) {
		return &ProxyError{Code: "invalid_realtime_call_id", Status: 400, Message: "Invalid realtime call ID"}
	}
	if request.Protocol != "" && request.Protocol != CodexRealtimeLive && request.Protocol != CodexRealtimeLegacy {
		return &ProxyError{Code: "invalid_realtime_protocol", Status: 400, Message: "Unsupported realtime protocol"}
	}
	if err := request.Headers.Validate(); err != nil {
		return err
	}
	if len(request.Query) > 16 {
		return &ProxyError{Code: "invalid_request", Status: 400, Message: "Realtime query is too large"}
	}
	for _, pair := range request.Query {
		if pair[0] == "call_id" || len(pair[0]) > 512 || len(pair[1]) > 512 {
			return &ProxyError{Code: "invalid_request", Status: 400, Message: "Invalid realtime query"}
		}
	}
	return nil
}

func (s *CodexOperations) Realtime(ctx context.Context, options CodexOperationOptions, target CodexOperationTarget, request CodexRealtimeRequest, connection CodexRealtimeConnection) error {
	if err := ValidateCodexRealtimeRequest(request); err != nil {
		return err
	}
	_, err := s.contentFree(ctx, options, target.Account, "realtime-live", "realtime_live", nil, target, func() (CodexOperationResult, error) {
		return CodexOperationResult{Status: 200, Body: []byte("{}"), ContentType: "application/json"}, s.provider.Realtime(ctx, target, request, connection)
	})
	return err
}

func realtimeCallID(location string) string {
	if location == "" || len(location) > 2048 {
		return ""
	}
	parsed, err := url.Parse(location)
	if err != nil {
		return ""
	}
	value := strings.Trim(parsed.EscapedPath(), "/")
	if value == "" {
		return ""
	}
	parts := strings.Split(value, "/")
	callID, err := url.PathUnescape(parts[len(parts)-1])
	if err != nil || !validRealtimeCallID(callID) {
		return ""
	}
	return callID
}

func validRealtimeCallID(callID string) bool {
	if callID == "" || callID == "." || callID == ".." || len(callID) > 256 {
		return false
	}
	for _, char := range callID {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '-' || char == '.' {
			continue
		}
		return false
	}
	return true
}

func resultHeaderValue(headers map[string][]string, name string) string {
	for key, values := range headers {
		if strings.EqualFold(key, name) && len(values) != 0 {
			return values[0]
		}
	}
	return ""
}
