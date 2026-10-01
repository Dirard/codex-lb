package application

import (
	"encoding/json"
	"strings"
)

const ResponsesLiteHeader = "x-openai-internal-codex-responses-lite"
const ResponsesLiteMetadataKey = "ws_request_header_x_openai_internal_codex_responses_lite"

// ResponsesLiteState belongs to one downstream WebSocket's sequential request
// loop. A prepared request or a hidden retry never establishes this trust.
type ResponsesLiteState struct {
	model, responseID string
}

func inputUsesResponsesLite(input []json.RawMessage) bool {
	for _, raw := range input {
		var item struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &item) == nil && item.Type == "additional_tools" {
			return true
		}
	}
	return false
}

// stripResponsesLiteMarker consumes only the reserved signal, preserving other
// metadata verbatim. Callers decide whether its claimed continuity is trusted.
func stripResponsesLiteMarker(object map[string]json.RawMessage) (map[string]json.RawMessage, bool, bool, error) {
	var metadata map[string]json.RawMessage
	if raw := object["client_metadata"]; len(raw) != 0 && json.Unmarshal(raw, &metadata) != nil {
		return nil, false, false, &ProxyError{Code: "invalid_request", Status: 400, Message: "client_metadata must be an object", Param: "client_metadata"}
	}
	marker, reserved := false, false
	for key, raw := range metadata {
		if strings.EqualFold(key, ResponsesLiteMetadataKey) {
			reserved = true
			var value string
			marker = marker || json.Unmarshal(raw, &value) == nil && strings.EqualFold(strings.TrimSpace(value), "true")
			delete(metadata, key)
		}
	}
	if reserved {
		writeResponsesLiteMetadata(object, metadata)
	}
	return metadata, marker, reserved, nil
}

func writeResponsesLiteMetadata(object map[string]json.RawMessage, metadata map[string]json.RawMessage) {
	if len(metadata) == 0 {
		delete(object, "client_metadata")
	} else {
		object["client_metadata"], _ = json.Marshal(metadata)
	}
}

func prepareResponsesLite(request *responseRequest, options ResponseOptions) error {
	_, marker, reserved, err := stripResponsesLiteMarker(request.Object)
	if err != nil {
		return err
	}
	request.WireClean = request.WireClean && !reserved
	state := options.LiteState
	if options.Transport == CapabilityTransportWebSocket && state != nil && marker &&
		state.responseID != "" && request.Previous == state.responseID && canonicalModel(request.Model) == state.model {
		request.ResponsesLite = true
	}
	return nil
}

func responsesLiteOutput(options ResponseOptions, model string, lite bool, emit func(ResponseEvent) error) func(ResponseEvent) error {
	if !lite || options.Transport != CapabilityTransportWebSocket || options.LiteState == nil || emit == nil {
		return emit
	}
	return func(event ResponseEvent) error {
		if err := emit(event); err != nil {
			return err
		}
		if event.Type == "response.created" {
			if id := responseEventID(event.Data); id != "" {
				options.LiteState.model, options.LiteState.responseID = canonicalModel(model), id
			}
		}
		return nil
	}
}

// NormalizeCodexResponsesLite derives the actual first-party wire signal. Only
// the application may authorize an inherited WebSocket marker; HTTP is always
// body-derived. This is also used by standalone and triggered compact requests.
func NormalizeCodexResponsesLite(object map[string]json.RawMessage, websocket, trusted bool) (bool, error) {
	metadata, _, _, err := stripResponsesLiteMarker(object)
	if err != nil {
		return false, err
	}
	var input []json.RawMessage
	_ = json.Unmarshal(object["input"], &input)
	lite := inputUsesResponsesLite(input) || websocket && trusted
	if !lite {
		return false, nil
	}
	if websocket {
		if metadata == nil {
			metadata = make(map[string]json.RawMessage)
		}
		metadata[ResponsesLiteMetadataKey] = jsonString("true")
		writeResponsesLiteMetadata(object, metadata)
	}
	var reasoning map[string]json.RawMessage
	if raw := object["reasoning"]; len(raw) != 0 && json.Unmarshal(raw, &reasoning) != nil {
		return false, &ProxyError{Code: "invalid_request", Status: 400, Message: "reasoning must be an object", Param: "reasoning"}
	}
	if reasoning == nil {
		reasoning = make(map[string]json.RawMessage)
	}
	reasoning["context"] = jsonString("all_turns")
	object["reasoning"], _ = json.Marshal(reasoning)
	return true, nil
}
