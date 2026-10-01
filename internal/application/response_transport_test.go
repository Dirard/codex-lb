package application

import (
	"testing"

	"codex-lb/internal/domain"
)

func TestNativeWebSocketKeepsExternalProtocolDefaults(t *testing.T) {
	options := ResponseOptions{Transport: CapabilityTransportWebSocket}
	settings := domain.RuntimeSettings{HTTPTransportPolicy: "always_websocket"}
	account := domain.Account{Kind: domain.AccountExternal}
	if ws, fallback := responseTransport(options, responseRequest{}, settings, domain.APIKey{}, account, nil, false); ws || fallback {
		t.Fatal("HTTP policy forced an external Responses source to support WebSocket")
	}
	settings.UpstreamStreamTransport = "websocket"
	if ws, fallback := responseTransport(options, responseRequest{}, settings, domain.APIKey{}, account, nil, false); !ws || fallback {
		t.Fatal("explicit source WS configuration was lost or made auto")
	}
}
