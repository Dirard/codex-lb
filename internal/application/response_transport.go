package application

import (
	"bytes"
	"encoding/json"
	"strings"

	"codex-lb/internal/domain"
)

// NativeCodexClient recognizes transport intent, never authentication or scope.
func NativeCodexClient(userAgent, originator string) bool {
	userAgent = strings.ToLower(strings.TrimSpace(userAgent))
	for _, prefix := range []string{"codex_cli_rs", "codex-tui", "codex_exec", "codex_sdk_ts", "codex_vscode", "codex desktop", "codex "} {
		if strings.HasPrefix(userAgent, prefix) {
			return true
		}
	}
	switch strings.TrimSpace(originator) {
	case "Codex Desktop", "codex_atlas", "codex_chatgpt_desktop", "codex_cli_rs", "codex_exec", "codex_sdk_ts", "codex_vscode":
		return true
	}
	return false
}

// responseTransport resolves explicit/client/catalog intent before dispatch.
// The second result permits only a proven pre-create handshake HTTP fallback.
func responseTransport(options ResponseOptions, request responseRequest, settings domain.RuntimeSettings, key domain.APIKey, account domain.Account, wire json.RawMessage, hydrated bool) (websocket, allowHTTPFallback bool) {
	if options.Transport == CapabilityTransportWebSocket {
		return account.Kind == domain.AccountChatGPT || settings.UpstreamStreamTransport == "websocket", false
	}
	switch settings.UpstreamStreamTransport {
	case "http":
		return false, false
	case "websocket":
		return true, false
	}
	if account.Kind != domain.AccountChatGPT || options.NativeCodexClient || !(request.PreferWebsockets || options.NativeTransportHint) {
		return false, false
	}
	policy := settings.HTTPTransportPolicy
	if key.TransportPolicyOverride != nil {
		policy = *key.TransportPolicyOverride
	}
	switch policy {
	case "always_http", "pinned":
		return false, false
	case "always_websocket":
	default: // Validated smart/default policy; derived locality is not a wire signal.
		cache, _ := explicitPromptCacheKey(request.Object)
		sticky := !hydrated && request.Previous != "" || cache != "" || options.SessionID != "" || options.ThreadID != "" || options.TurnState != ""
		if !sticky {
			return false, false
		}
	}
	if autoTransportRequiresHTTP(wire) {
		return false, false
	}
	return true, true
}

func autoTransportRequiresHTTP(wire json.RawMessage) bool {
	// Leave the same 2 MiB envelope margin below the 16 MiB WS frame budget.
	if len(wire) > 14<<20 {
		return true
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(wire))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return true // Validation belongs to the existing request boundary.
	}
	return transportContainsImage(value)
}

func transportContainsImage(value any) bool {
	switch value := value.(type) {
	case map[string]any:
		kind, _ := value["type"].(string)
		if kind == "input_image" || kind == "image_url" || kind == "image_generation" {
			return true
		}
		for _, child := range value {
			if transportContainsImage(child) {
				return true
			}
		}
	case []any:
		for _, child := range value {
			if transportContainsImage(child) {
				return true
			}
		}
	}
	return false
}
