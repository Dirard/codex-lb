package upstream

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestWireReasoningAliasesOnlyEffortFields(t *testing.T) {
	for _, pair := range [][2]string{
		{`{"reasoning":{"effort":" ULTRA ","summary":"auto","context":"all_turns"},"keep":null}`, `{"reasoning":{"effort":"max","summary":"auto","context":"all_turns"},"keep":null}`},
		{`{"reasoning_effort":"ultra","reasoningEffort":"Ultra","thinking":{"effort":"ultra","type":"enabled"}}`, `{"reasoning_effort":"max","reasoningEffort":"max","thinking":{"effort":"max","type":"enabled"}}`},
		{`{"thinking":"ultra","input":"ultra"}`, `{"thinking":"max","input":"ultra"}`},
		{`{"reasoning":{"effort":"minimal"},"thinking":true,"extra":{"effort":"ultra"}}`, `{"reasoning":{"effort":"minimal"},"thinking":true,"extra":{"effort":"ultra"}}`},
		{`{"reasoning":null,"reasoning_effort":12,"thinking":[]}`, `{"reasoning":null,"reasoning_effort":12,"thinking":[]}`},
	} {
		var object, want map[string]json.RawMessage
		_ = json.Unmarshal([]byte(pair[0]), &object)
		_ = json.Unmarshal([]byte(pair[1]), &want)
		NormalizeWireReasoning(object)
		var gotValue, wantValue any
		_ = json.Unmarshal(mustJSON(object), &gotValue)
		_ = json.Unmarshal(mustJSON(want), &wantValue)
		if !reflect.DeepEqual(gotValue, wantValue) || NormalizeWireReasoning(object) {
			t.Fatalf("wire alias changed unrelated fields or was not idempotent: %s", mustJSON(object))
		}
	}
	// Provider overrides are applied before final wire aliasing.
	cap := ResponsesCapabilities()
	cap.ExtraBody = map[string]json.RawMessage{"reasoning": json.RawMessage(`{"effort":"ultra","summary":"auto"}`)}
	body, err := passthroughBody(responsesWireRequest{Raw: json.RawMessage(`{"model":"test","input":"hello"}`)}, cap)
	if err != nil || !json.Valid(body) {
		t.Fatalf("Responses override: %s %v", body, err)
	}
	var object map[string]json.RawMessage
	_ = json.Unmarshal(body, &object)
	if string(object["reasoning"]) != `{"effort":"max","summary":"auto"}` {
		t.Fatalf("override was not aliased: %s", body)
	}
	cap.ExtraBody = map[string]json.RawMessage{"reasoning_effort": json.RawMessage(`"ultra"`)}
	body, err = mapRequestBody(ChatRequest{}, cap)
	_ = json.Unmarshal(body, &object)
	if err != nil || string(object["reasoning_effort"]) != `"max"` {
		t.Fatalf("Chat override was not aliased: %s %v", body, err)
	}
}
