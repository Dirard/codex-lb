package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

func (h *CodexOperationsHandler) registerPublicWarmupRoutes(mux *http.ServeMux) {
	for _, path := range []string{"/v1/warmup", "/v1/warmup/{$}", "/v1/warmup/{mode}", "/v1/warmup/{mode}/{$}"} {
		mux.HandleFunc("POST "+path, h.publicWarmup)
	}
}

// publicWarmup authenticates a real bearer key before accepting either the
// body-mode or URL-mode form of the pinned public warmup operation.
func (h *CodexOperationsHandler) publicWarmup(w http.ResponseWriter, r *http.Request) {
	key, err := h.publicWarmupKey(r)
	if err != nil {
		writeProxyError(w, err)
		return
	}
	if len(r.Header.Values(domain.RequiredCapabilityHeader)) != 0 {
		writeProxyError(w, &application.ProxyError{Code: "required_capability_transport_unsupported", Status: 400, Message: "Required capability routing is only supported over the Responses WebSocket transport"})
		return
	}
	mode := r.PathValue("mode")
	if mode == "" {
		body, err := readOperationJSON(w, r, 1024)
		if err != nil {
			writeProxyError(w, err)
			return
		}
		var object map[string]json.RawMessage
		if json.Unmarshal(body, &object) != nil || object == nil || len(object) > 1 {
			writeProxyError(w, &application.ProxyError{Code: "invalid_request", Status: 400, Message: "Invalid warmup request"})
			return
		}
		mode = "normal"
		if raw, ok := object["mode"]; ok {
			var requested string
			if json.Unmarshal(raw, &requested) != nil || strings.TrimSpace(requested) == "" {
				writeProxyError(w, &application.ProxyError{Code: "invalid_request", Status: 400, Message: "Invalid warmup mode"})
				return
			}
			mode = requested
		} else if len(object) != 0 {
			writeProxyError(w, &application.ProxyError{Code: "invalid_request", Status: 400, Message: "Invalid warmup request"})
			return
		}
	} else {
		body, err := readOperationBody(w, r, 1024)
		if err != nil {
			writeProxyError(w, err)
			return
		}
		if len(bytes.TrimSpace(body)) != 0 {
			writeProxyError(w, &application.ProxyError{Code: "invalid_request", Status: 400, Message: "URL-mode warmup does not accept a body"})
			return
		}
	}
	options := operationOptions(r, key.ID)
	options.SessionID, options.TurnState, options.ConversationID = "", "", ""
	summary, err := h.ops.PublicWarmup(r.Context(), options, mode)
	if err != nil {
		writeProxyError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

func (h *CodexOperationsHandler) publicWarmupKey(r *http.Request) (domain.APIKey, error) {
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		return domain.APIKey{}, invalidKey()
	}
	parts := strings.Fields(values[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || len(parts[1]) > 4096 {
		return domain.APIKey{}, invalidKey()
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(parts[1])))
	key, err := h.store.FindAPIKeyByHash(r.Context(), hash)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.APIKey{}, invalidKey()
	}
	if err != nil {
		return domain.APIKey{}, err
	}
	if domain.IsInternalKey(key.ID) || !key.IsActive || key.ExpiresAt != nil && !key.ExpiresAt.After(time.Now()) {
		return domain.APIKey{}, invalidKey()
	}
	return key, nil
}
