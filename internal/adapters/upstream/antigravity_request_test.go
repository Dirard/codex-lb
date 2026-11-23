package upstream

import (
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func antigravityTarget(serverURL string) Target {
	return Target{
		ProviderID: "antigravity", AccountID: "source-1", KeyID: "key-1",
		BaseURL: serverURL, Credential: "access-token",
		AntigravityProject:   "aicode-consumers",
		AntigravityUserAgent: "antigravity/hub/2.34.0 (aidev_client; os_type=linux; arch=amd64)",
		Capabilities:         AntigravityCapabilities(),
	}
}

// antigravityEnvelopeFor translates a Responses body without a network call.
func antigravityEnvelopeFor(t *testing.T, target Target, body map[string]any, state Continuation, lookup func([]string) map[string]string) (map[string]any, antigravityPlan) {
	t.Helper()
	plan, err := antigravityPlanFor(target, body, state, lookup)
	if err != nil {
		t.Fatal(err)
	}
	return decodeBody(t, plan.body), plan
}

func antigravityPlanFor(target Target, body map[string]any, state Continuation, lookup func([]string) map[string]string) (antigravityPlan, error) {
	prepared, err := prepareTarget(target)
	if err != nil {
		return antigravityPlan{}, err
	}
	req, items, catalog, err := prepareRequest(Request{Body: mustJSON(body)}, prepared)
	if err != nil {
		return antigravityPlan{}, err
	}
	return buildAntigravityRequest(prepared, req, items, state, catalog, lookup)
}

func assertJSONEqual(t *testing.T, label string, got any, want string) {
	t.Helper()
	var expected any
	if err := json.Unmarshal([]byte(want), &expected); err != nil {
		t.Fatalf("%s: bad expectation: %v", label, err)
	}
	var actual any
	if err := json.Unmarshal(mustJSON(got), &actual); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("%s:\n got %s\nwant %s", label, mustJSON(actual), mustJSON(expected))
	}
}

func shellTool() map[string]any {
	return map[string]any{"type": "function", "name": "shell", "description": "Run a command",
		"parameters": map[string]any{"type": "object", "additionalProperties": false,
			"properties": map[string]any{"cmd": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}},
			"required":   []string{"cmd"}}}
}

func TestAntigravityEnvelopeTranslatesConversation(t *testing.T) {
	target := antigravityTarget("http://127.0.0.1")
	target.ModelMappings = map[string]string{"flash": "gemini-3.8-flash-high"}
	envelope, _ := antigravityEnvelopeFor(t, target, map[string]any{
		"model": "flash", "instructions": "Be brief.", "max_output_tokens": 2048, "temperature": 0.5,
		"reasoning": map[string]any{"effort": "high"}, "tools": []any{shellTool()},
		"input": []any{
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "input_text", "text": "Look"},
				map[string]any{"type": "input_image", "image_url": "data:image/png;base64,AAAA"},
				map[string]any{"type": "input_text", "text": ""},
			}},
			map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "Calling"}}},
			map[string]any{"type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "secret thoughts"}}},
			map[string]any{"type": "function_call", "call_id": "call_1", "name": "shell", "arguments": `{"cmd":["ls"]}`},
			map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "file.txt"},
			map[string]any{"role": "user", "content": ""},
			map[string]any{"role": "user", "content": "Thanks"},
		},
	}, Continuation{}, nil)

	for field, want := range map[string]string{"project": "aicode-consumers", "model": "gemini-3.8-flash-high", "userAgent": "antigravity", "requestType": "agent"} {
		if envelope[field] != want {
			t.Fatalf("%s = %v, want %q", field, envelope[field], want)
		}
	}
	if id, _ := envelope["requestId"].(string); !regexp.MustCompile(`^agent-[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(id) {
		t.Fatalf("requestId = %q", id)
	}
	request := envelope["request"].(map[string]any)
	assertJSONEqual(t, "systemInstruction", request["systemInstruction"], `{"role":"user","parts":[{"text":"Be brief."}]}`)
	assertJSONEqual(t, "contents", request["contents"], `[
		{"role":"user","parts":[{"text":"Look"},{"inlineData":{"mimeType":"image/png","data":"AAAA"}}]},
		{"role":"model","parts":[{"text":"Calling"},{"functionCall":{"id":"call_1","name":"shell","args":{"cmd":["ls"]}},"thoughtSignature":"skip_thought_signature_validator"}]},
		{"role":"user","parts":[{"functionResponse":{"id":"call_1","name":"shell","response":{"output":"file.txt"}}},{"text":"Thanks"}]}
	]`)
	assertJSONEqual(t, "tools", request["tools"], `[{"functionDeclarations":[{"name":"shell","description":"Run a command",
		"parameters":{"type":"object","properties":{"cmd":{"type":"array","items":{"type":"string"}}},"required":["cmd"]}}]}]`)
	assertJSONEqual(t, "generationConfig", request["generationConfig"], `{"maxOutputTokens":2048,"temperature":0.5,"thinkingConfig":{"includeThoughts":true,"thinkingLevel":"high"}}`)
	if _, ok := request["toolConfig"]; ok {
		t.Fatalf("Gemini auto tool choice sent a toolConfig: %v", request["toolConfig"])
	}
	if session, _ := request["sessionId"].(string); !strings.HasPrefix(session, "-") {
		t.Fatalf("sessionId = %q, want a negative decimal", session)
	} else if _, err := strconv.ParseInt(session, 10, 64); err != nil {
		t.Fatalf("sessionId = %q: %v", session, err)
	}
	if strings.Contains(string(mustJSON(envelope)), "secret thoughts") {
		t.Fatal("thought text was sent back to Google")
	}
	if strings.Contains(string(mustJSON(envelope)), "labels") {
		t.Fatal("native-only labels field was sent")
	}
}

func TestAntigravityEnvelopeKeepsNativeFieldOrder(t *testing.T) {
	_, plan := antigravityEnvelopeFor(t, antigravityTarget("http://127.0.0.1"), map[string]any{
		"model": "gemini-3.8-flash-high", "instructions": "sys", "input": "hi", "tools": []any{shellTool()}, "max_output_tokens": 10,
	}, Continuation{}, nil)
	body := string(plan.body)
	order := []string{`"project"`, `"requestId"`, `"model"`, `"userAgent"`, `"requestType"`, `"request"`,
		`"contents"`, `"systemInstruction"`, `"tools"`, `"generationConfig"`, `"sessionId"`}
	last := -1
	for _, field := range order {
		index := strings.Index(body, field)
		if index <= last {
			t.Fatalf("field %s out of order in %s", field, body)
		}
		last = index
	}
}

func TestAntigravityEnvelopeRefusesUnsupportedImages(t *testing.T) {
	for name, url := range map[string]string{
		"remote":     "https://example.com/cat.png",
		"not base64": "data:image/svg+xml,<svg/>",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := antigravityPlanFor(antigravityTarget("http://127.0.0.1"), map[string]any{
				"model": "gemini-3.8-flash-high",
				"input": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_image", "image_url": url}}}},
			}, Continuation{}, nil)
			var failure *Error
			if !errors.As(err, &failure) || failure.Code != ErrorCodeUnsupportedCapability {
				t.Fatalf("err = %v, want unsupported capability", err)
			}
		})
	}
}

func TestAntigravityEnvelopeMergesTurnsAndDropsEmptyParts(t *testing.T) {
	envelope, _ := antigravityEnvelopeFor(t, antigravityTarget("http://127.0.0.1"), map[string]any{
		"model": "gemini-3.8-flash-high", "tools": []any{shellTool()},
		"input": []any{
			map[string]any{"role": "user", "content": "one"},
			map[string]any{"role": "user", "content": "two"},
			map[string]any{"type": "function_call", "call_id": "call_a", "name": "shell"},
			map[string]any{"type": "function_call", "call_id": "call_b", "name": "shell", "arguments": `{"cmd":["pwd"]}`},
			map[string]any{"role": "user", "content": "interjection"},
			map[string]any{"type": "function_call_output", "call_id": "call_a", "output": []any{
				map[string]any{"type": "input_text", "text": "see image"},
				map[string]any{"type": "input_image", "image_url": "data:image/jpeg;base64,BBBB"},
			}},
			map[string]any{"type": "function_call_output", "call_id": "call_b", "output": ""},
			map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": ""}}},
		},
	}, Continuation{}, nil)
	request := envelope["request"].(map[string]any)
	assertJSONEqual(t, "contents", request["contents"], `[
		{"role":"user","parts":[{"text":"one"},{"text":"two"}]},
		{"role":"model","parts":[
			{"functionCall":{"id":"call_a","name":"shell","args":{}},"thoughtSignature":"skip_thought_signature_validator"},
			{"functionCall":{"id":"call_b","name":"shell","args":{"cmd":["pwd"]}},"thoughtSignature":"skip_thought_signature_validator"}]},
		{"role":"user","parts":[
			{"functionResponse":{"id":"call_a","name":"shell","response":{"output":"see image"}}},
			{"functionResponse":{"id":"call_b","name":"shell","response":{"output":""}}},
			{"text":"interjection"},
			{"inlineData":{"mimeType":"image/jpeg","data":"BBBB"}}]}
	]`)
	if _, ok := request["systemInstruction"]; ok {
		t.Fatal("empty system instruction was sent")
	}
	if _, ok := request["generationConfig"].(map[string]any)["thinkingConfig"]; !ok {
		t.Fatal("thoughts were not requested without an effort")
	}
}

func TestAntigravityClaudeToolsAndChoice(t *testing.T) {
	tools := []any{
		map[string]any{"type": "function", "name": "noop"},
		map[string]any{"type": "function", "name": "pick", "parameters": map[string]any{"type": "object", "properties": map[string]any{
			"value": map[string]any{"anyOf": []any{map[string]any{"type": "integer"}, map[string]any{"type": "string"}}},
		}}},
	}
	cases := []struct {
		name, model string
		choice      any
		want        string
	}{
		{"claude auto", "claude-sonnet-5-5-high", nil, `{"functionCallingConfig":{"mode":"VALIDATED"}}`},
		{"claude required", "claude-sonnet-5-5-high", "required", `{"functionCallingConfig":{"mode":"ANY"}}`},
		{"gemini none", "gemini-3.8-flash-high", "none", `{"functionCallingConfig":{"mode":"NONE"}}`},
		{"gemini required", "gemini-3.8-flash-high", "required", `{"functionCallingConfig":{"mode":"ANY"}}`},
		{"gemini named", "gemini-3.8-flash-high", map[string]any{"type": "function", "name": "pick"}, `{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["pick"]}}`},
		{"gemini auto", "gemini-3.8-flash-high", "auto", `null`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]any{"model": tc.model, "input": "hi", "tools": tools}
			if tc.choice != nil {
				body["tool_choice"] = tc.choice
			}
			envelope, plan := antigravityEnvelopeFor(t, antigravityTarget("http://127.0.0.1"), body, Continuation{}, nil)
			request := envelope["request"].(map[string]any)
			assertJSONEqual(t, "toolConfig", request["toolConfig"], tc.want)
			declarations := request["tools"].([]any)[0].(map[string]any)["functionDeclarations"]
			if plan.claude {
				assertJSONEqual(t, "declarations", declarations, `[
					{"name":"noop","parameters":{"type":"object","properties":{"_placeholder":{"type":"boolean"}}}},
					{"name":"pick","parameters":{"type":"object","properties":{"value":{"type":"integer"}}}}]`)
			} else {
				assertJSONEqual(t, "declarations", declarations, `[
					{"name":"noop"},
					{"name":"pick","parameters":{"type":"object","properties":{"value":{"anyOf":[{"type":"integer"},{"type":"string"}]}}}}]`)
			}
		})
	}
}

func TestAntigravityThinkingMapping(t *testing.T) {
	limit := func(value uint64) *uint64 { return &value }
	cases := []struct {
		name      string
		effort    string
		maxOutput *uint64
		claude    bool
		want      string
	}{
		{"gemini none", "none", nil, false, `null`},
		{"gemini default", "", nil, false, `{"includeThoughts":true}`},
		{"gemini minimal", "minimal", nil, false, `{"includeThoughts":true,"thinkingLevel":"minimal"}`},
		{"gemini low", "low", nil, false, `{"includeThoughts":true,"thinkingLevel":"low"}`},
		{"gemini medium", "medium", nil, false, `{"includeThoughts":true,"thinkingLevel":"medium"}`},
		{"gemini high", "high", nil, false, `{"includeThoughts":true,"thinkingLevel":"high"}`},
		{"gemini xhigh", "xhigh", nil, false, `{"includeThoughts":true,"thinkingLevel":"xhigh"}`},
		{"gemini ultra", "ultra", nil, false, `{"includeThoughts":true,"thinkingLevel":"xhigh"}`},
		{"claude default", "", nil, true, `{"includeThoughts":true}`},
		{"claude low", "low", nil, true, `{"includeThoughts":true,"thinkingBudget":1024}`},
		{"claude medium", "medium", nil, true, `{"includeThoughts":true,"thinkingBudget":4096}`},
		{"claude high", "high", limit(64000), true, `{"includeThoughts":true,"thinkingBudget":16384}`},
		{"claude xhigh", "xhigh", nil, true, `{"includeThoughts":true,"thinkingBudget":32768}`},
		{"claude clamped", "high", limit(8000), true, `{"includeThoughts":true,"thinkingBudget":7999}`},
		{"claude too small", "low", limit(1024), true, `null`},
		{"claude none", "none", nil, true, `null`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertJSONEqual(t, "thinkingConfig", antigravityThinking(tc.effort, tc.maxOutput, tc.claude), tc.want)
		})
	}
}

func TestAntigravitySessionIDIsStablePerConversation(t *testing.T) {
	target := antigravityTarget("http://127.0.0.1")
	first := []ChatMessage{{Role: "system", Content: mustJSON("sys")}, {Role: "user", Content: mustJSON("start here")}}
	later := append(append([]ChatMessage(nil), first...),
		ChatMessage{Role: "assistant", Content: mustJSON("ok")}, ChatMessage{Role: "user", Content: mustJSON("next")})
	id := antigravitySessionID(target, first)
	if antigravitySessionID(target, later) != id {
		t.Fatal("session id changed within a conversation")
	}
	if antigravitySessionID(target, []ChatMessage{{Role: "user", Content: mustJSON("other conversation")}}) == id {
		t.Fatal("different conversations share a session id")
	}
	other := target
	other.AccountID = "source-2"
	if antigravitySessionID(other, first) == id {
		t.Fatal("different accounts share a session id")
	}
	withSession := target
	withSession.SessionID = "codex-session"
	if antigravitySessionID(withSession, first) != antigravitySessionID(withSession, []ChatMessage{{Role: "user", Content: mustJSON("x")}}) {
		t.Fatal("client session id did not decide the session id")
	}
	if value, err := strconv.ParseInt(id, 10, 64); err != nil || value >= 0 {
		t.Fatalf("session id %q is not a negative int64", id)
	}
}

func TestAntigravityTurnKeysMatchAcrossHistoryShapes(t *testing.T) {
	// A continuation keeps one assistant message with text and calls; a full
	// replay splits it into a text message and a calls message.
	call := ChatToolCall{ID: "call_1", Type: "function", Function: ChatToolCallFunction{Name: "shell", Arguments: `{"cmd":["ls"]}`}}
	continuation := []ChatMessage{
		{Role: "user", Content: mustJSON("hi")},
		{Role: "assistant", Content: mustJSON("Calling"), ToolCalls: []ChatToolCall{call}, ReasoningContent: "thinking"},
		{Role: "tool", ToolCallID: "call_1", Content: mustJSON("out")},
		{Role: "assistant", Content: mustJSON("done")},
	}
	replay := []ChatMessage{
		{Role: "user", Content: mustJSON("hi")},
		{Role: "assistant", Content: mustJSON("Calling")},
		{Role: "assistant", ToolCalls: []ChatToolCall{call}},
		{Role: "tool", ToolCallID: "call_1", Content: mustJSON("out")},
		{Role: "assistant", Content: mustJSON("done")},
	}
	_, left, err := antigravityContents(continuation)
	if err != nil {
		t.Fatal(err)
	}
	_, right, err := antigravityContents(replay)
	if err != nil {
		t.Fatal(err)
	}
	leftKeys, leftHash := antigravityTurnKeys(left)
	rightKeys, rightHash := antigravityTurnKeys(right)
	if !reflect.DeepEqual(leftKeys, rightKeys) || leftHash != rightHash || len(leftKeys) != 2 {
		t.Fatalf("turn keys differ: %v %v", leftKeys, rightKeys)
	}
	_, prefix, _ := antigravityContents(continuation[:3])
	_, history := antigravityTurnKeys(prefix)
	if history.turnKey("done") != leftKeys[3] {
		t.Fatal("the stored key of an answer does not match its replayed turn")
	}
}

func TestCleanAntigravitySchema(t *testing.T) {
	schema := `{
		"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"x","title":"Args","type":"object","additionalProperties":false,
		"$defs":{"Mode":{"type":"string","enum":["a","b"],"title":"Mode"},
			"Node":{"type":"object","properties":{"child":{"$ref":"#/$defs/Node"}}}},
		"properties":{
			"mode":{"$ref":"#/$defs/Mode","description":"which"},
			"count":{"type":["integer","null"],"default":3,"minimum":0,"exclusiveMinimum":0,"format":"int32","x-order":1,"examples":[1]},
			"level":{"type":"integer","enum":[1,2]},
			"tags":{"type":"array"},
			"kind":{"const":"fixed"},
			"url":{"type":"string","format":"uri","patternProperties":{"x":{}}},
			"choice":{"anyOf":[{"type":"string"},{"type":"null"}]},
			"union":{"oneOf":[{"type":"string"},{"type":"integer"}]},
			"merged":{"allOf":[{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]},{"properties":{"b":{"type":"number"}},"required":["b"]}]},
			"empty":{"type":"object","additionalProperties":{"type":"string"}},
			"node":{"$ref":"#/$defs/Node"},
			"missing":{"$ref":"#/$defs/Absent"},
			"anything":{}
		},
		"required":["mode","ghost"]
	}`
	cases := []struct {
		name   string
		claude bool
		want   string
	}{
		{"gemini", false, `{"type":"object","properties":{
			"mode":{"type":"string","enum":["a","b"],"description":"which"},
			"count":{"type":"integer","minimum":0,"format":"int32"},
			"level":{"type":"integer","enum":["1","2"]},
			"tags":{"type":"array","items":{"type":"string"}},
			"kind":{"type":"string","enum":["fixed"]},
			"url":{"type":"string"},
			"choice":{"type":"string"},
			"union":{"anyOf":[{"type":"string"},{"type":"integer"}]},
			"merged":{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"number"}},"required":["a","b"]},
			"empty":{"type":"object"},
			"node":{"type":"object","properties":{"child":{"type":"object"}}},
			"missing":{"type":"object"},
			"anything":{"type":"string"}
		},"required":["mode"]}`},
		{"claude", true, `{"type":"object","properties":{
			"mode":{"type":"string","enum":["a","b"],"description":"which"},
			"count":{"type":"integer","minimum":0,"format":"int32"},
			"level":{"type":"integer","enum":["1","2"]},
			"tags":{"type":"array","items":{"type":"string"}},
			"kind":{"type":"string","enum":["fixed"]},
			"url":{"type":"string"},
			"choice":{"type":"string"},
			"union":{"type":"string"},
			"merged":{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"number"}},"required":["a","b"]},
			"empty":{"type":"object","properties":{"_placeholder":{"type":"boolean"}}},
			"node":{"type":"object","properties":{"child":{"type":"object","properties":{"_placeholder":{"type":"boolean"}}}}},
			"missing":{"type":"object","properties":{"_placeholder":{"type":"boolean"}}},
			"anything":{"type":"string"}
		},"required":["mode"]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cleaned, err := cleanAntigravitySchema(json.RawMessage(schema), tc.claude)
			if err != nil {
				t.Fatal(err)
			}
			assertJSONEqual(t, "schema", json.RawMessage(cleaned), tc.want)
		})
	}
	for name, raw := range map[string]string{"absent": ``, "empty object": `{"type":"object","properties":{}}`} {
		gemini, err := cleanAntigravitySchema(json.RawMessage(raw), false)
		if err != nil || gemini != nil {
			t.Fatalf("%s: Gemini parameters = %s, %v; want none", name, gemini, err)
		}
		claude, err := cleanAntigravitySchema(json.RawMessage(raw), true)
		if err != nil {
			t.Fatal(err)
		}
		assertJSONEqual(t, name, json.RawMessage(claude), `{"type":"object","properties":{"_placeholder":{"type":"boolean"}}}`)
	}
	if _, err := cleanAntigravitySchema(json.RawMessage(`{`), false); err == nil {
		t.Fatal("invalid schema JSON was accepted")
	}
}

func TestCleanAntigravitySchemaBoundsDeepRefs(t *testing.T) {
	defs := map[string]any{}
	for i := 0; i < 20; i++ {
		defs["D"+strconv.Itoa(i)] = map[string]any{"type": "object", "properties": map[string]any{
			"next": map[string]any{"$ref": "#/$defs/D" + strconv.Itoa(i+1)},
		}}
	}
	raw := mustJSON(map[string]any{"$defs": defs, "type": "object", "properties": map[string]any{"root": map[string]any{"$ref": "#/$defs/D0"}}})
	cleaned, err := cleanAntigravitySchema(raw, false)
	if err != nil {
		t.Fatal(err)
	}
	if depth := strings.Count(string(cleaned), `"next"`); depth != antigravityRefMaxDepth {
		t.Fatalf("inlined %d nested refs, want %d", depth, antigravityRefMaxDepth)
	}
}
