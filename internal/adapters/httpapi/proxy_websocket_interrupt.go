package httpapi

import (
	"context"
	"encoding/json"
	"time"

	"codex-lb/internal/application"
	"github.com/coder/websocket"
)

func parseWebSocketInterrupt(object map[string]json.RawMessage) (string, error) {
	var responseID, mode string
	if len(object) != 3 || json.Unmarshal(object["response_id"], &responseID) != nil || responseID == "" || len(responseID) > 512 ||
		json.Unmarshal(object["mode"], &mode) != nil || mode != "discard_partial_items" {
		return "", &application.ProxyError{Code: "invalid_request", Status: 400, Message: "response.interrupt requires response_id and mode discard_partial_items"}
	}
	return responseID, nil
}

func sendWebSocketInterruptFailure(ctx context.Context, connection *websocket.Conn, responseID string) error {
	// A stale/unsupported control must not become a fatal error for the active response.
	payload, _ := json.Marshal(struct {
		Type       string            `json:"type"`
		ResponseID string            `json:"response_id"`
		Error      map[string]string `json:"error"`
	}{"response.interrupt.failed", responseID, map[string]string{"code": "response_interrupt_unavailable", "message": "This connection has no interruptible response with that ID"}})
	writeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return connection.Write(writeCtx, websocket.MessageText, payload)
}
