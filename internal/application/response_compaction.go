package application

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"codex-lb/internal/domain"
)

// NormalizeCodexCompactOutput keeps the encrypted summary and its identity,
// excluding historical messages echoed by remote compaction v2.
func NormalizeCodexCompactOutput(body []byte) ([]byte, json.RawMessage) {
	var response map[string]json.RawMessage
	if json.Unmarshal(body, &response) != nil || response == nil {
		return body, nil
	}
	var output []json.RawMessage
	_ = json.Unmarshal(response["output"], &output)
	var selected json.RawMessage
	for _, raw := range output {
		var item struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &item) == nil && (item.Type == "compaction" || item.Type == "compaction_summary") {
			if selected = compactOutputItem(raw); selected != nil {
				break
			}
		}
	}
	if selected == nil {
		// The private in-band endpoint can wrap its encrypted summary in the
		// last message instead of an explicit compaction item (original contract).
		for index := len(output) - 1; index >= 0; index-- {
			var message struct {
				Type    string          `json:"type"`
				Text    string          `json:"text"`
				Content json.RawMessage `json:"content"`
			}
			if json.Unmarshal(output[index], &message) != nil || message.Type != "message" {
				continue
			}
			text := message.Text
			if text == "" {
				var parts []struct {
					Text string `json:"text"`
				}
				if json.Unmarshal(message.Content, &parts) != nil {
					var part struct {
						Text string `json:"text"`
					}
					if json.Unmarshal(message.Content, &part) == nil {
						text = part.Text
					}
				} else {
					var combined strings.Builder
					for _, part := range parts {
						combined.WriteString(part.Text)
					}
					text = combined.String()
				}
			}
			if text != "" {
				var item map[string]json.RawMessage
				_ = json.Unmarshal(output[index], &item)
				item["encrypted_content"], _ = json.Marshal(text)
				raw, _ := json.Marshal(item)
				selected = compactOutputItem(raw)
				break
			}
		}
	}
	if selected == nil {
		selected = compactOutputItem(response["compaction_summary"])
	}
	if selected == nil {
		return body, nil
	}
	response["output"], _ = json.Marshal([]json.RawMessage{selected})
	var object string
	_ = json.Unmarshal(response["object"], &object)
	if !strings.HasPrefix(object, "response.compact") {
		response["object"] = json.RawMessage(`"response.compaction"`)
	}
	normalized, err := json.Marshal(response)
	if err != nil {
		return body, nil
	}
	return normalized, selected
}

func compactOutputItem(raw json.RawMessage) json.RawMessage {
	var source map[string]json.RawMessage
	if json.Unmarshal(raw, &source) != nil || source == nil {
		return nil
	}
	var encrypted string
	if value := source["encrypted_content"]; len(value) == 0 || string(value) == "null" || json.Unmarshal(value, &encrypted) != nil {
		return nil
	}
	item := map[string]json.RawMessage{"type": json.RawMessage(`"compaction"`), "encrypted_content": source["encrypted_content"]}
	var id, status string
	if json.Unmarshal(source["id"], &id) == nil && strings.HasPrefix(id, "cmp_") {
		item["id"] = source["id"]
	}
	if json.Unmarshal(source["status"], &status) == nil && strings.TrimSpace(status) != "" {
		item["status"] = source["status"]
	}
	encoded, _ := json.Marshal(item)
	return encoded
}

// prepareCompact validates and selects retained input before resolving owners.
func prepareCompact(ctx context.Context, body json.RawMessage, key domain.APIKey, settings domain.RuntimeSettings, codexTrigger bool) (json.RawMessage, string, error) {
	if len(body) > maxCompactBody || !json.Valid(body) {
		return nil, "", &ProxyError{Code: "invalid_request", Status: 400, Message: "Invalid compact request"}
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(body, &object); err != nil || object == nil {
		return nil, "", &ProxyError{Code: "invalid_request", Status: 400, Message: "Invalid compact request"}
	}
	for _, field := range []string{"max_output_tokens", "metadata", "prompt_cache_retention", "safety_identifier", "temperature", "top_p", "truncation", "user"} {
		delete(object, field)
	}
	policyBody, _ := json.Marshal(object)
	request, err := parseResponse(policyBody, key, settings, codexTrigger)
	if err != nil {
		return nil, "", err
	}
	object = request.Object
	if _, err := NormalizeCodexResponsesLite(object, false, false); err != nil {
		return nil, "", err
	}
	if err := request.loadInput(); err != nil {
		return nil, "", err
	}
	input := request.Input
	if len(input) > 0 {
		var terminal struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(input[len(input)-1], &terminal)
		if terminal.Type == "compaction_trigger" {
			input[len(input)-1] = json.RawMessage(`{"type":"compaction_trigger"}`)
		}
	}
	anchored := strings.TrimSpace(request.Previous) != "" || strings.TrimSpace(compactString(object["conversation"])) != ""
	input, err = trimCompactHistory(ctx, input, anchored)
	if err != nil {
		return nil, "", err
	}
	object["input"], _ = json.Marshal(input)
	object["store"] = json.RawMessage("false")
	encoded, err := json.Marshal(object)
	if err != nil {
		return nil, "", &ProxyError{Code: "invalid_request", Status: 400, Message: "Invalid compact request"}
	}
	return encoded, request.Previous, nil
}

func compactTriggerBody(request responseRequest) (json.RawMessage, error) {
	selected := make(map[string]json.RawMessage, 9)
	for _, field := range []string{"model", "instructions", "reasoning", "store", "service_tier", "prompt_cache_key", "previous_response_id", "conversation"} {
		if value, ok := request.Object[field]; ok && string(value) != "null" {
			selected[field] = value
		}
	}
	if selected["prompt_cache_key"] == nil {
		var alias string
		if json.Unmarshal(request.Object["promptCacheKey"], &alias) == nil {
			selected["prompt_cache_key"] = jsonString(alias)
		}
	}
	input := append([]json.RawMessage(nil), request.Input...)
	input[len(input)-1] = json.RawMessage(`{"type":"compaction_trigger"}`)
	selected["input"], _ = json.Marshal(input)
	body, err := json.Marshal(selected)
	if err != nil || len(body) > maxCompactBody {
		return nil, &ProxyError{Code: "responses_compact_input_too_large", Status: 400, Message: "Compact input exceeds the upstream size limit", Param: "input"}
	}
	return body, nil
}

func compactInputSize(input []json.RawMessage) int {
	encoded, _ := json.Marshal(input)
	size := 0
	quoted, escaped := false, false
	for len(encoded) != 0 {
		r, width := utf8.DecodeRune(encoded)
		encoded = encoded[width:]
		switch {
		case r <= 127:
			size++
		case r <= 0xffff:
			size += 6
		default:
			size += 12
		}
		if quoted {
			switch {
			case escaped:
				escaped = false
			case r == '\\':
				escaped = true
			case r == '"':
				quoted = false
			}
		} else if r == '"' {
			quoted = true
		} else if r == ',' || r == ':' {
			size++ // Python's compact estimator includes spaces after separators.
		}
	}
	return size
}

var compactInlineImageURL = regexp.MustCompile("data:image/[^,\\s]+,[^\\s\\\"'<>]+")

func elideCompactImages(value any) (any, bool) {
	switch value := value.(type) {
	case map[string]any:
		kind, _ := value["type"].(string)
		url, _ := value["image_url"].(string)
		if kind == "image_url" {
			if nested, ok := value["image_url"].(map[string]any); ok {
				url, _ = nested["url"].(string)
			}
		}
		if strings.HasPrefix(url, "data:image/") && (kind == "input_image" || kind == "image_url") {
			textKind := "input_text"
			if kind == "image_url" {
				textKind = "text"
			}
			return map[string]any{"type": textKind, "text": compactImageMarker(url)}, true
		}
		changed := false
		for field, child := range value {
			var replaced bool
			value[field], replaced = elideCompactImages(child)
			changed = changed || replaced
		}
		return value, changed
	case []any:
		changed := false
		for index, child := range value {
			var replaced bool
			value[index], replaced = elideCompactImages(child)
			changed = changed || replaced
		}
		return value, changed
	case string:
		if compactInlineImageURL.MatchString(value) {
			return compactInlineImageURL.ReplaceAllStringFunc(value, compactImageMarker), true
		}
	}
	return value, false
}

func compactImageMarker(url string) string {
	return fmt.Sprintf("[compact trim] Omitted inline image bytes that were already observed before compaction (%d encoded characters).", len(url))
}

func (p *Proxy) compactTriggered(ctx context.Context, options ResponseOptions, request responseRequest, emit func(ResponseEvent) error) (ResponseResult, error) {
	if p.compact == nil {
		return ResponseResult{}, &ProxyError{Code: "compact_unavailable", Status: 503, Message: "Codex compaction is unavailable"}
	}
	if err := request.loadInput(); err != nil {
		return ResponseResult{}, err
	}
	body, err := compactTriggerBody(request)
	if err != nil {
		return ResponseResult{}, err
	}
	result, err := p.compact.CompactWithOptions(ctx, CodexOperationOptions{
		KeyID: options.KeyID, SessionID: options.SessionID, TurnState: options.TurnState,
		ThreadID: options.ThreadID, ClientAffinity: options.ClientAffinity, SynthesizedTurnState: options.SynthesizedTurnState,
		ConversationID: options.ConversationID, UserAgent: options.UserAgent, UserAgentGroup: options.UserAgentGroup,
		ClientIP: options.ClientIP, Transport: options.Transport, CodexResponsesTrigger: true, OnDispatch: options.OnDispatch,
	}, body)
	if err != nil {
		return ResponseResult{}, err
	}
	if result.Failed || result.Status < 200 || result.Status >= 300 {
		code, status := result.ErrorCode, result.Status
		if code == "" {
			code = "upstream_error"
		}
		if status < 400 || status > 599 {
			status = 502
		}
		return ResponseResult{}, &ProviderFailure{Code: code, Status: status, Dispatched: true}
	}
	var response struct {
		ID    string          `json:"id"`
		Usage json.RawMessage `json:"usage"`
	}
	if json.Unmarshal(result.Body, &response) != nil || response.ID == "" {
		return ResponseResult{}, &ProviderFailure{Code: "invalid_upstream_response", Status: 502, Dispatched: true}
	}
	_, item := NormalizeCodexCompactOutput(result.Body)
	if item == nil {
		return ResponseResult{}, &ProviderFailure{Code: "invalid_upstream_response", Status: 502, Dispatched: true}
	}
	var done map[string]json.RawMessage
	_ = json.Unmarshal(item, &done)
	if done["status"] == nil {
		done["status"] = jsonString("completed")
	}
	added := make(map[string]json.RawMessage, len(done))
	for field, value := range done {
		added[field] = value
	}
	added["status"] = jsonString("in_progress")
	completed := map[string]any{"id": response.ID, "object": "response", "status": "completed", "output": []any{done}}
	var usage map[string]json.RawMessage
	if json.Unmarshal(response.Usage, &usage) == nil && usage != nil {
		completed["usage"] = usage
	}
	for _, event := range []struct {
		kind   string
		fields map[string]any
	}{
		{"response.created", map[string]any{"sequence_number": 0, "response": map[string]any{"id": response.ID, "object": "response", "status": "in_progress", "output": []any{}}}},
		{"response.output_item.added", map[string]any{"sequence_number": 1, "output_index": 0, "item": added}},
		{"response.output_item.done", map[string]any{"sequence_number": 2, "output_index": 0, "item": done}},
		{"response.completed", map[string]any{"sequence_number": 3, "response": completed}},
	} {
		payload := map[string]any{"type": event.kind}
		for name, value := range event.fields {
			payload[name] = value
		}
		encoded, _ := json.Marshal(payload)
		if err := emit(ResponseEvent{Type: event.kind, Data: encoded}); err != nil {
			return ResponseResult{}, err
		}
	}
	responseBody, _ := json.Marshal(completed)
	return ResponseResult{ResponseID: response.ID, Response: responseBody, Usage: result.Usage, ServiceTier: result.ServiceTier, OutputObserved: true, DoneMarker: true}, nil
}

func compactResponseID(body []byte) []byte {
	var response map[string]json.RawMessage
	if json.Unmarshal(body, &response) != nil || response == nil {
		return body
	}
	var id string
	if json.Unmarshal(response["id"], &id) == nil && id != "" {
		return body
	}
	response["id"] = jsonString("resp_" + rand.Text())
	encoded, err := json.Marshal(response)
	if err != nil {
		return body
	}
	return encoded
}
