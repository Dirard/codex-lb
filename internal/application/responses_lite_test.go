package application

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"codex-lb/internal/domain"
)

const liteTestPrefix = `[{"type":"additional_tools","role":"developer","tools":[{"type":"function","name":"read","parameters":{"type":"object","properties":{"file_id":{"type":"string"}},"large":9007199254740993}}]},{"type":"message","role":"developer","content":"Keep this instruction in input"},{"role":"user","content":"hello"}]`

func TestResponsesLiteTrustFollowsCreatedNotPreparationOrTerminal(t *testing.T) {
	state := &ResponsesLiteState{}
	options := ResponseOptions{Transport: CapabilityTransportWebSocket, LiteState: state}
	prepare := func(model, previous, input, metadata string, options ResponseOptions) responseRequest {
		t.Helper()
		body := `{"model":` + string(jsonString(model)) + `,"previous_response_id":` + string(jsonString(previous)) + `,"input":` + input + `,"client_metadata":` + metadata + `}`
		request, err := parseResponse([]byte(body), domain.APIKey{}, domain.RuntimeSettings{}, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := prepareResponsesLite(&request, options); err != nil {
			t.Fatal(err)
		}
		return request
	}
	marker := `{"WS_REQUEST_HEADER_X_OPENAI_INTERNAL_CODEX_RESPONSES_LITE":" TrUe ","keep":9007199254740993}`
	request := prepare("gpt-5.6", "", liteTestPrefix, marker, options)
	if !request.ResponsesLite || state.responseID != "" || strings.Contains(string(request.Object["client_metadata"]), "RESPONSES_LITE") {
		t.Fatal("preparation established trust or kept the untrusted marker")
	}
	output := responsesLiteOutput(options, request.Model, true, func(ResponseEvent) error { return nil })
	_ = output(ResponseEvent{Type: "response.completed", Data: []byte(`{"response":{"id":"terminal-only"}}`)})
	if state.responseID != "" {
		t.Fatal("terminal event established Lite trust")
	}
	_ = output(ResponseEvent{Type: "response.created", Data: []byte(`{"response":{"id":"accepted"}}`)})
	for _, test := range []struct {
		name, model, previous, metadata string
		options                         ResponseOptions
		want                            bool
	}{
		{"alias", "gpt-5.6-sol", "accepted", marker, options, true},
		{"different model", "gpt-6-sol", "accepted", marker, options, false},
		{"different response", "gpt-5.6-sol", "old", marker, options, false},
		{"fresh request", "gpt-5.6-sol", "", marker, options, false},
		{"no marker", "gpt-5.6-sol", "accepted", `{}`, options, false},
		{"boolean marker", "gpt-5.6-sol", "accepted", `{"` + ResponsesLiteMetadataKey + `":true}`, options, false},
		{"another connection", "gpt-5.6-sol", "accepted", marker, ResponseOptions{Transport: CapabilityTransportWebSocket, LiteState: &ResponsesLiteState{}}, false},
		{"HTTP", "gpt-5.6-sol", "accepted", marker, ResponseOptions{LiteState: state}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := prepare(test.model, test.previous, `[]`, test.metadata, test.options)
			if request.ResponsesLite != test.want || state.responseID != "accepted" {
				t.Fatalf("unexpected trust: lite=%v state=%+v", request.ResponsesLite, state)
			}
			if _, err := NormalizeCodexResponsesLite(request.Object, true, request.ResponsesLite); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(request.Object["client_metadata"]), ResponsesLiteMetadataKey) != test.want {
				t.Fatal("actual WebSocket marker disagrees with trust")
			}
		})
	}
	for _, lite := range []bool{false, true} {
		err := responsesLiteOutput(options, "other", lite, func(ResponseEvent) error { return errors.New("disconnected") })(ResponseEvent{Type: "response.created", Data: []byte(`{"response":{"id":"not-visible"}}`)})
		if err == nil || state.responseID != "accepted" {
			t.Fatal("failed downstream write changed trust")
		}
	}
	_ = responsesLiteOutput(options, "other", false, func(ResponseEvent) error { return nil })(ResponseEvent{Type: "response.created", Data: []byte(`{"response":{"id":"ordinary"}}`)})
	if state.responseID != "accepted" {
		t.Fatal("non-Lite acceptance cleared trust")
	}
}

func TestResponsesLiteFreshReplayKeepsOnlySelfContainedTypedHistory(t *testing.T) {
	var prefix []json.RawMessage
	_ = json.Unmarshal([]byte(liteTestPrefix), &prefix)
	history := responseContext{Items: append(prefix,
		json.RawMessage(`{"type":"function_call","call_id":"same","name":"read","arguments":"{}"}`),
		json.RawMessage(`{"type":"custom_tool_call","call_id":"same","name":"apply_patch","input":"patch"}`),
		json.RawMessage(`{"type":"function_call_output","call_id":"same","output":"result"}`),
		json.RawMessage(`{"type":"custom_tool_call_output","call_id":"same","output":"done"}`),
		json.RawMessage(`{"type":"apply_patch_call","call_id":"same","operation":{"type":"delete_file","path":"temp.txt"}}`))}
	request, err := parseResponse([]byte(`{"model":"gpt-6-sol","previous_response_id":"old","input":[{"type":"apply_patch_call_output","call_id":"same","output":"done"}],"client_metadata":{"`+ResponsesLiteMetadataKey+`":"true","keep":"yes"}}`), domain.APIKey{}, domain.RuntimeSettings{}, false)
	if err != nil {
		t.Fatal(err)
	}
	body, err := replayBody(request, history)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"additional_tools", "Keep this instruction", "9007199254740993", "apply_patch_call_output", `"keep":"yes"`} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("replay lost %s", want)
		}
	}
	if strings.Contains(string(body), "previous_response_id") || strings.Contains(string(body), ResponsesLiteMetadataKey) {
		t.Fatal("fresh replay retained previous-owner state")
	}
	for _, bundle := range []string{
		`{"type":"additional_tools","role":"developer","tools":[{"type":"file_search","vector_store_ids":["old-store"]}]}`,
		`{"type":"additional_tools","role":"developer","tools":[{"type":"function","name":"read","container_id":"old-container"}]}`,
		`{"type":"additional_tools","role":"developer","tools":[{"type":"mcp","server_url":"https://example.test"}]}`,
		`{"type":"additional_tools","role":"user","tools":[]}`,
	} {
		invalid := responseContext{Items: append([]json.RawMessage{json.RawMessage(bundle)}, history.Items[1:]...)}
		if _, err := replayBody(request, invalid); err == nil {
			t.Fatal("account-bound or malformed tool bundle replayed")
		}
	}
	request.Input = []json.RawMessage{json.RawMessage(`{"type":"function_call_output","call_id":"same","output":"wrong protocol"}`)}
	request.DeferredInput = false
	if _, err := replayBody(request, history); err == nil {
		t.Fatal("mismatched tool-output protocol consumed apply-patch call")
	}
	request.Input = []json.RawMessage{json.RawMessage(`{"type":"apply_patch_call_output","call_id":"same","output":"done"}`)}
	request.DeferredInput = false
	for _, operation := range []string{
		`{"type":"apply_patch_call","call_id":"same","operation":{"type":"delete_file","path":"temp.txt","opaque_owner_state":"old-owner"}}`,
		`{"type":"apply_patch_call","call_id":"same","operation":{"type":"delete_file","path":"temp.txt"},"container_id":"old-container"}`,
	} {
		history.Items[len(history.Items)-1] = json.RawMessage(operation)
		if _, err := replayBody(request, history); err == nil {
			t.Fatal("opaque or provider-bound apply-patch state replayed")
		}
	}
}

func TestResponsesLiteWireUsesBodyNotUntrustedMetadata(t *testing.T) {
	for _, ws := range []bool{false, true} {
		for _, prefix := range []bool{false, true} {
			input := `[]`
			if prefix {
				input = liteTestPrefix
			}
			var object map[string]json.RawMessage
			_ = json.Unmarshal([]byte(`{"input":`+input+`,"instructions":"unchanged","reasoning":{"effort":"high","context":"last_turn"},"client_metadata":{"`+strings.ToUpper(ResponsesLiteMetadataKey)+`":"true","keep":9007199254740993}}`), &object)
			lite, err := NormalizeCodexResponsesLite(object, ws, false)
			if err != nil || lite != prefix || string(object["input"]) != input || string(object["instructions"]) != `"unchanged"` {
				t.Fatalf("wire normalization changed context: lite=%v error=%v", lite, err)
			}
			if strings.Contains(string(object["client_metadata"]), ResponsesLiteMetadataKey) != (ws && prefix) ||
				!strings.Contains(string(object["client_metadata"]), "9007199254740993") ||
				strings.Contains(string(object["reasoning"]), "all_turns") != prefix {
				t.Fatal("wire marker or reasoning context mismatch")
			}
		}
	}
	for _, raw := range []string{`[]`, `true`, `"opaque"`} {
		if _, _, _, err := stripResponsesLiteMarker(map[string]json.RawMessage{"client_metadata": []byte(raw)}); err == nil {
			t.Fatal("malformed client metadata accepted")
		}
	}
}

func TestResponsesLiteModelCapabilityMustBeExplicitlyFalse(t *testing.T) {
	for _, raw := range []string{"false", "true", "null", `"false"`, ""} {
		snapshot := &domain.CatalogSnapshot{MetadataModels: map[string]domain.CatalogModel{
			"gpt-5.6-sol": {Raw: map[string]json.RawMessage{"use_responses_lite": []byte(raw)}},
		}}
		if rejected := catalogRejectsResponsesLite(snapshot, "gpt-5.6"); rejected != (raw == "false") {
			t.Fatalf("capability %q became rejection=%v", raw, rejected)
		}
	}
}
