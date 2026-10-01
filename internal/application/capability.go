package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"

	"codex-lb/internal/domain"
)

const (
	CapabilityTransportHTTP      = "http"
	CapabilityTransportWebSocket = "websocket"
	CapabilityResponseCreate     = "response.create"
)

type CapabilityIntent struct {
	RequiresTrustedCyber bool
}

type CapabilityRoute struct {
	RequireSecurityWorkAuthorized bool
	Aliases                       []domain.CapabilityLineageAlias
}

type CapabilitySignalRequest struct {
	Transport           string
	FrameType           string
	APIKeyID            string
	HeaderValues        []string
	Body                json.RawMessage
	SessionID           string
	ThreadID            string
	TurnState           string
	PreviousResponseIDs []string
	ParentTaskIDs       []string
	WindowIDs           []string
}

type CapabilitySignal struct {
	Intent  CapabilityIntent
	Aliases []domain.CapabilityLineageAlias
	Payload json.RawMessage
}

type CapabilityLineageStore interface {
	IsCapabilityRequired(ctx context.Context, capability, keyScope string, aliases []domain.CapabilityLineageAlias) (bool, error)
	RequireCapability(ctx context.Context, capability, keyScope string, aliases []domain.CapabilityLineageAlias) ([]string, error)
}

type CapabilityRouter struct {
	store CapabilityLineageStore
}

func NewCapabilityRouter(store CapabilityLineageStore) *CapabilityRouter {
	return &CapabilityRouter{store: store}
}

// PrepareCapability validates raw ingress before request normalization and
// restores any durable requirement before account selection.
func (p *Proxy) PrepareCapability(ctx context.Context, options ResponseOptions, raw json.RawMessage) (CapabilitySignal, CapabilityRoute, error) {
	transport := options.Transport
	if transport == "" {
		transport = CapabilityTransportHTTP
	}
	signal, err := ParseCapabilitySignal(CapabilitySignalRequest{
		Transport: transport, APIKeyID: options.KeyID, HeaderValues: options.CapabilityHeaderValues,
		Body: raw, SessionID: options.SessionID, ThreadID: options.ThreadID, TurnState: options.TurnState,
		ParentTaskIDs: options.ParentTaskIDs, WindowIDs: options.WindowIDs,
	})
	if err != nil {
		return CapabilitySignal{}, CapabilityRoute{}, err
	}
	if p.Capabilities == nil {
		return CapabilitySignal{}, CapabilityRoute{}, capabilityUnavailable()
	}
	if transport != CapabilityTransportWebSocket && signal.Intent.RequiresTrustedCyber {
		return CapabilitySignal{}, CapabilityRoute{}, capabilityTransportUnsupported()
	}
	route, err := p.Capabilities.Route(ctx, signal.Intent, options.KeyID, signal.Aliases)
	if err != nil {
		return CapabilitySignal{}, CapabilityRoute{}, err
	}
	if route.RequireSecurityWorkAuthorized && transport != CapabilityTransportWebSocket {
		return CapabilitySignal{}, CapabilityRoute{}, capabilityTransportUnsupported()
	}
	return signal, route, nil
}

func ParseCapabilitySignal(request CapabilitySignalRequest) (CapabilitySignal, error) {
	pairs, err := rawJSONObjectPairs(request.Body)
	if err != nil {
		return CapabilitySignal{}, capabilityBadRequest("invalid_request", "Invalid capability-bearing request payload")
	}
	frameType := request.FrameType
	if frameType == "" {
		frameType = jsonObjectString(pairs, "type")
	}

	var markerValues []json.RawMessage
	for _, pair := range pairs {
		if strings.EqualFold(pair.Key, domain.RequiredCapabilityHeader) {
			markerValues = append(markerValues, pair.Value)
		}
	}
	misplaced := len(markerValues) != 0
	var metadataObjects []json.RawMessage
	for _, pair := range pairs {
		if pair.Key == "client_metadata" {
			metadataObjects = append(metadataObjects, pair.Value)
		}
	}
	duplicateMetadata := len(metadataObjects) > 1
	for _, metadata := range metadataObjects {
		metadataPairs, err := rawJSONObjectPairs(metadata)
		if err != nil {
			return CapabilitySignal{}, capabilityBadRequest("unsupported_required_capability", "Required routing capability is unsupported")
		}
		for _, pair := range metadataPairs {
			if strings.EqualFold(pair.Key, domain.RequiredCapabilityHeader) {
				markerValues = append(markerValues, pair.Value)
			}
		}
	}
	signals := len(request.HeaderValues) + len(markerValues)
	if duplicateMetadata && signals != 0 {
		return CapabilitySignal{}, capabilityBadRequest("unsupported_required_capability", "Required routing capability is unsupported")
	}
	if signals == 0 {
		return CapabilitySignal{Payload: request.Body, Aliases: capabilityAliases(request, metadataObjects, pairs)}, nil
	}
	if frameType != "" && frameType != CapabilityResponseCreate {
		return CapabilitySignal{}, capabilityBadRequest("unsupported_required_capability", "Required routing capability is unsupported")
	}
	if request.APIKeyID == "" || request.APIKeyID == domain.LocalProxyKeyID {
		return CapabilitySignal{}, capabilityForbidden("capability_signal_untrusted", "Required capability signal requires an authenticated proxy API key")
	}
	if signals != 1 || misplaced || duplicateMetadata {
		return CapabilitySignal{}, capabilityBadRequest("unsupported_required_capability", "Required routing capability is unsupported")
	}
	var raw string
	if len(request.HeaderValues) == 1 {
		raw = request.HeaderValues[0]
	} else if err := json.Unmarshal(markerValues[0], &raw); err != nil {
		raw = ""
	}
	if raw == domain.TrustedCyberCapability && request.Transport != CapabilityTransportWebSocket {
		return CapabilitySignal{}, capabilityTransportUnsupported()
	}
	if raw != domain.TrustedCyberCapability || frameType != CapabilityResponseCreate {
		return CapabilitySignal{}, capabilityBadRequest("unsupported_required_capability", "Required routing capability is unsupported")
	}
	var payload json.RawMessage
	if len(request.HeaderValues) == 1 {
		payload = request.Body
	} else {
		var err error
		payload, err = stripCapabilityMetadata(request.Body, metadataObjects)
		if err != nil {
			return CapabilitySignal{}, capabilityBadRequest("invalid_request", "Invalid capability-bearing request payload")
		}
	}
	return CapabilitySignal{
		Intent:  CapabilityIntent{RequiresTrustedCyber: true},
		Aliases: capabilityAliases(request, metadataObjects, pairs),
		Payload: payload,
	}, nil
}

func (r *CapabilityRouter) Route(ctx context.Context, intent CapabilityIntent, keyScope string, aliases []domain.CapabilityLineageAlias) (CapabilityRoute, error) {
	aliases = domain.NormalizeCapabilityAliases(aliases)
	if keyScope == domain.LocalProxyKeyID {
		if intent.RequiresTrustedCyber {
			return CapabilityRoute{}, capabilityForbidden("capability_signal_untrusted", "Required capability signal requires an authenticated proxy API key")
		}
		return CapabilityRoute{Aliases: aliases}, nil
	}
	if keyScope == "" {
		return CapabilityRoute{}, capabilityUnavailable()
	}
	if len(aliases) == 0 {
		return CapabilityRoute{RequireSecurityWorkAuthorized: intent.RequiresTrustedCyber, Aliases: aliases}, nil
	}
	if r == nil || r.store == nil {
		return CapabilityRoute{}, capabilityUnavailable()
	}
	required := intent.RequiresTrustedCyber
	if !required {
		found, err := r.store.IsCapabilityRequired(ctx, domain.TrustedCyberCapability, keyScope, aliases)
		if err != nil {
			return CapabilityRoute{}, capabilityUnavailable()
		}
		required = found
	}
	if required {
		if hashes, err := r.store.RequireCapability(ctx, domain.TrustedCyberCapability, keyScope, aliases); err != nil || len(hashes) != len(aliases) {
			return CapabilityRoute{}, capabilityUnavailable()
		}
	}
	return CapabilityRoute{RequireSecurityWorkAuthorized: required, Aliases: aliases}, nil
}

func (r *CapabilityRouter) MarkResponseCreated(ctx context.Context, keyScope string, responseIDs ...string) error {
	if keyScope == domain.LocalProxyKeyID {
		return capabilityForbidden("capability_signal_untrusted", "Required capability signal requires an authenticated proxy API key")
	}
	if len(responseIDs) == 0 {
		return nil
	}
	if keyScope == "" || r == nil || r.store == nil {
		return capabilityUnavailable()
	}
	aliases := make([]domain.CapabilityLineageAlias, 0, len(responseIDs))
	for _, id := range responseIDs {
		aliases = append(aliases, domain.CapabilityLineageAlias{Kind: "previous_response", Value: id})
	}
	aliases = domain.NormalizeCapabilityAliases(aliases)
	if len(aliases) != len(responseIDs) {
		return capabilityUnavailable()
	}
	if hashes, err := r.store.RequireCapability(ctx, domain.TrustedCyberCapability, keyScope, aliases); err != nil || len(hashes) != len(aliases) {
		return capabilityUnavailable()
	}
	return nil
}

func FilterCapabilityAccounts(route CapabilityRoute, candidates []domain.Account, establishedOwnerID string) ([]domain.Account, error) {
	if !route.RequireSecurityWorkAuthorized {
		return candidates, nil
	}
	if establishedOwnerID != "" {
		for _, account := range candidates {
			if account.ID == establishedOwnerID && account.Kind == domain.AccountChatGPT && account.SecurityWorkAuthorized {
				return []domain.Account{account}, nil
			}
		}
		return nil, noSecurityAccounts()
	}
	result := make([]domain.Account, 0, len(candidates))
	for _, account := range candidates {
		if account.Kind == domain.AccountChatGPT && account.SecurityWorkAuthorized {
			result = append(result, account)
		}
	}
	if len(result) == 0 {
		return nil, noSecurityAccounts()
	}
	return result, nil
}

type capabilityPair struct {
	Key   string
	Value json.RawMessage
}

type discardedJSON struct{}

func (discardedJSON) UnmarshalJSON([]byte) error { return nil }

func relevantCapabilityField(key string) bool {
	return strings.EqualFold(key, domain.RequiredCapabilityHeader) || key == "type" ||
		key == "client_metadata" || key == "turn_state" || key == "previous_response_id" ||
		key == "x-codex-parent-thread-id" || key == "x-codex-window-id"
}

func rawJSONObjectPairs(raw json.RawMessage) ([]capabilityPair, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	open, err := decoder.Token()
	if err != nil || open != json.Delim('{') {
		return nil, domain.ErrInvalid
	}
	result := make([]capabilityPair, 0, 8)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, domain.ErrInvalid
		}
		if relevantCapabilityField(key) {
			var value json.RawMessage
			if err := decoder.Decode(&value); err != nil {
				return nil, err
			}
			result = append(result, capabilityPair{Key: key, Value: value})
			continue
		}
		var discard discardedJSON
		if err := decoder.Decode(&discard); err != nil {
			return nil, err
		}
	}
	if close, err := decoder.Token(); err != nil || close != json.Delim('}') {
		return nil, domain.ErrInvalid
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, domain.ErrInvalid
	}
	return result, nil
}

func jsonObjectString(pairs []capabilityPair, key string) string {
	for _, pair := range pairs {
		if pair.Key != key {
			continue
		}
		var value string
		if json.Unmarshal(pair.Value, &value) == nil {
			return value
		}
	}
	return ""
}

func stripCapabilityMetadata(raw json.RawMessage, metadataObjects []json.RawMessage) (json.RawMessage, error) {
	if len(metadataObjects) != 1 {
		return append(json.RawMessage(nil), raw...), nil
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil || top == nil {
		return nil, err
	}
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(metadataObjects[0], &metadata); err != nil || metadata == nil {
		return nil, err
	}
	for key := range metadata {
		if strings.EqualFold(key, domain.RequiredCapabilityHeader) {
			delete(metadata, key)
		}
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	top["client_metadata"] = encoded
	return json.Marshal(top)
}

func capabilityAliases(request CapabilitySignalRequest, metadataObjects []json.RawMessage, bodyPairs []capabilityPair) []domain.CapabilityLineageAlias {
	values := []domain.CapabilityLineageAlias{
		{Kind: "session_header", Value: request.SessionID},
		{Kind: "thread_header", Value: request.ThreadID},
		{Kind: "turn_state", Value: request.TurnState},
	}
	for _, value := range jsonObjectStrings(bodyPairs, "turn_state") {
		values = append(values, domain.CapabilityLineageAlias{Kind: "turn_state", Value: value})
	}
	for _, value := range jsonObjectStrings(bodyPairs, "previous_response_id") {
		values = append(values, domain.CapabilityLineageAlias{Kind: "previous_response", Value: value})
	}
	for _, id := range request.PreviousResponseIDs {
		values = append(values, domain.CapabilityLineageAlias{Kind: "previous_response", Value: id})
	}
	for _, id := range request.ParentTaskIDs {
		values = append(values, domain.CapabilityLineageAlias{Kind: "codex_task", Value: id})
	}
	for _, id := range request.WindowIDs {
		values = append(values, domain.CapabilityLineageAlias{Kind: "codex_window", Value: id})
		values = append(values, domain.CapabilityLineageAlias{Kind: "codex_task", Value: stableWindowTask(id)})
	}
	for _, metadata := range metadataObjects {
		pairs, _ := rawJSONObjectPairs(metadata) // Already validated at ingress.
		for _, name := range []string{"x-codex-parent-thread-id", "x-codex-window-id"} {
			for _, value := range jsonObjectStrings(pairs, name) {
				if name == "x-codex-window-id" {
					values = append(values, domain.CapabilityLineageAlias{Kind: "codex_window", Value: value})
					values = append(values, domain.CapabilityLineageAlias{Kind: "codex_task", Value: stableWindowTask(value)})
				} else {
					values = append(values, domain.CapabilityLineageAlias{Kind: "codex_task", Value: value})
				}
			}
		}
	}
	return domain.NormalizeCapabilityAliases(values)
}

var terminalWindowSlot = regexp.MustCompile(`:\d+$`)

func stableWindowTask(value string) string {
	return terminalWindowSlot.ReplaceAllString(strings.TrimSpace(value), "")
}

func jsonObjectStrings(pairs []capabilityPair, name string) []string {
	var result []string
	for _, pair := range pairs {
		if !strings.EqualFold(pair.Key, name) {
			continue
		}
		var value string
		if json.Unmarshal(pair.Value, &value) == nil && strings.TrimSpace(value) != "" {
			result = append(result, strings.TrimSpace(value))
		}
	}
	return result
}

func responseEventID(raw json.RawMessage) string {
	var value struct {
		Response struct {
			ID string `json:"id"`
		} `json:"response"`
	}
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value.Response.ID
}

func capabilityBadRequest(code, message string) error {
	return &ProxyError{Code: code, Status: 400, Message: message}
}

func capabilityForbidden(code, message string) error {
	return &ProxyError{Code: code, Status: 403, Message: message}
}

func capabilityUnavailable() error {
	return &ProxyError{Code: "capability_lineage_unavailable", Status: 503, Message: "Required capability lineage is unavailable; retry later"}
}

func capabilityTransportUnsupported() error {
	return capabilityBadRequest("required_capability_transport_unsupported", "Required capability routing is only supported over the Responses WebSocket transport")
}

func noSecurityAccounts() error {
	return &ProxyError{Code: "no_security_work_authorized_accounts", Status: 503, Message: "No accounts marked as authorized for security work; ordinary-account fallback did not occur"}
}
