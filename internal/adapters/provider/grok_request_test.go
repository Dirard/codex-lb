package provider

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"codex-lb/internal/adapters/upstream"
	"codex-lb/internal/application"
)

// grokCodexBody has the fields Codex CLI 0.165.4 sends with fallback model
// metadata, which xAI refused on 2026-11-02 in this form.
const grokCodexBody = `{"model":"grok-4.7","stream":true,"store":false,"include":["reasoning.encrypted_content"],
"prompt_cache_key":"thread","client_metadata":{"session_id":"thread"},"tool_choice":"auto","parallel_tool_calls":true,
"reasoning":{"effort":"max","summary":"detailed"},
"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"<b>hi</b>"}]},
 {"type":"function_call","call_id":"call_1","name":"list_agents","namespace":"agents","arguments":"{}"},
 {"type":"function_call_output","call_id":"call_1","output":"[]"}],
"tools":[{"type":"function","name":"exec_command","parameters":{"type":"object"}},
 {"type":"namespace","name":"agents","description":"Agents","tools":[{"type":"function","name":"list_agents","description":"List","parameters":{"type":"object"}},{"type":"function","name":"spawn_agent","parameters":{"type":"object"}}]},
 {"type":"web_search","external_web_access":true,"filters":{"allowed_domains":["go.dev"]},"search_context_size":"high"}]}`

func decodeGrokBody(t *testing.T, raw json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatalf("adapted body is not JSON: %v", err)
	}
	return object
}

func TestGrokRequestFlattensNamespacesAndKeepsOtherFields(t *testing.T) {
	body, names, err := adaptGrokRequest(json.RawMessage(grokCodexBody), "grok-4.7")
	if err != nil {
		t.Fatal(err)
	}
	object := decodeGrokBody(t, body)
	var tools []map[string]any
	if err := json.Unmarshal(object["tools"], &tools); err != nil {
		t.Fatal(err)
	}
	var kinds, toolNames []string
	for _, tool := range tools {
		kinds = append(kinds, tool["type"].(string))
		if name, ok := tool["name"].(string); ok {
			toolNames = append(toolNames, name)
		}
	}
	if strings.Join(kinds, ",") != "function,function,function,web_search" ||
		strings.Join(toolNames, ",") != "exec_command,agents-list_agents,agents-spawn_agent" {
		t.Fatalf("tools: %s", object["tools"])
	}
	if tools[1]["description"] != "List" || tools[1]["parameters"] == nil {
		t.Fatalf("child fields were lost: %v", tools[1])
	}
	if len(tools[3]) != 2 || tools[3]["filters"] == nil {
		t.Fatalf("web search kept unsupported fields: %v", tools[3])
	}
	if names["agents-spawn_agent"] != (grokNamespacedTool{Namespace: "agents", Name: "spawn_agent"}) || len(names) != 2 {
		t.Fatalf("names: %v", names)
	}
	var input []map[string]any
	if err := json.Unmarshal(object["input"], &input); err != nil {
		t.Fatal(err)
	}
	if input[1]["name"] != "agents-list_agents" || input[1]["namespace"] != nil {
		t.Fatalf("history call: %v", input[1])
	}
	if string(object["reasoning"]) != `{"effort":"xhigh","summary":"detailed"}` {
		t.Fatalf("reasoning: %s", object["reasoning"])
	}
	for _, field := range []string{"include", "prompt_cache_key", "client_metadata", "store", "stream", "tool_choice", "parallel_tool_calls"} {
		if object[field] == nil {
			t.Fatalf("%s was dropped", field)
		}
	}
	if !strings.Contains(string(object["input"]), `"<b>hi</b>"`) {
		t.Fatalf("message text was re-escaped: %s", object["input"])
	}
}

func TestGrokRequestFitsReasoningToTheModel(t *testing.T) {
	for _, test := range []struct {
		model, effort, want string
	}{
		{"grok-4.7", "max", `{"effort":"xhigh","summary":"auto"}`},
		{"grok-4.7", "ultra", `{"effort":"xhigh","summary":"auto"}`},
		{"grok-4.6", "medium", `{"effort":"medium","summary":"auto"}`},
		{"grok-4.5", "xhigh", `{"effort":"high","summary":"auto"}`},
		{"grok-4.5", "minimal", `{"effort":"low","summary":"auto"}`},
		{"grok-4.7", "none", `{"effort":"low","summary":"auto"}`},
		{"grok-build-0.1", "high", ""},
		{"grok-4.3", "low", ""},
		{"grok-4.20-0309-reasoning", "max", ""},
	} {
		body := `{"model":"` + test.model + `","input":"hi","reasoning":{"effort":"` + test.effort + `","summary":"auto"}}`
		adapted, _, err := adaptGrokRequest(json.RawMessage(body), test.model)
		if err != nil {
			t.Fatal(err)
		}
		if got := string(decodeGrokBody(t, adapted)["reasoning"]); got != test.want {
			t.Fatalf("%s %s: reasoning %q, want %q", test.model, test.effort, got, test.want)
		}
	}
}

func TestGrokRequestDropsCachedOnlySearchAndRejectsBadNamespaces(t *testing.T) {
	adapted, _, err := adaptGrokRequest(json.RawMessage(`{"model":"grok-4.7","input":"hi","tools":[{"type":"web_search","external_web_access":false}]}`), "grok-4.7")
	if err != nil || string(decodeGrokBody(t, adapted)["tools"]) != "[]" {
		t.Fatalf("cached-only search: %s %v", adapted, err)
	}
	long := strings.Repeat("n", 40)
	adapted, names, err := adaptGrokRequest(json.RawMessage(`{"model":"grok-4.7","input":"hi","tools":[{"type":"namespace","name":"mcp__`+long+`","tools":[{"type":"function","name":"`+long+`"}]}]}`), "grok-4.7")
	if err != nil {
		t.Fatal(err)
	}
	for flat, tool := range names {
		if len(flat) != grokMaxToolName || !strings.HasPrefix(flat, "mcp__nnn") || tool.Name != long || !strings.Contains(string(adapted), flat) {
			t.Fatalf("long name %q for %+v", flat, tool)
		}
	}
	for name, body := range map[string]string{
		"custom child": `{"model":"m","tools":[{"type":"namespace","name":"ns","tools":[{"type":"custom","name":"x"}]}]}`,
		"collision":    `{"model":"m","tools":[{"type":"function","name":"ns-x"},{"type":"namespace","name":"ns","tools":[{"type":"function","name":"x"}]}]}`,
		"no name":      `{"model":"m","tools":[{"type":"namespace","tools":[{"type":"function","name":"x"}]}]}`,
	} {
		_, _, err := adaptGrokRequest(json.RawMessage(body), "grok-4.7")
		var failure *application.ProviderFailure
		if !errors.As(err, &failure) || failure.Dispatched {
			t.Fatalf("%s: %v", name, err)
		}
		if name == "custom child" && failure.Code != upstream.ErrorCodeUnsupportedCapability {
			t.Fatalf("custom child code: %s", failure.Code)
		}
	}
}

func TestGrokAnswersGetTheirNamespacesBack(t *testing.T) {
	names := grokToolNames{"agents-list_agents": {Namespace: "agents", Name: "list_agents"}}
	call := `{"type":"function_call","id":"fc_1","call_id":"call_1","name":"agents-list_agents","arguments":"{}"}`
	want := `{"arguments":"{}","call_id":"call_1","id":"fc_1","name":"list_agents","namespace":"agents","type":"function_call"}`
	done := names.restoreEvent("response.output_item.done", json.RawMessage(`{"type":"response.output_item.done","output_index":1,"item":`+call+`}`))
	if !strings.Contains(string(done), want) {
		t.Fatalf("output item: %s", done)
	}
	completed := names.restoreEvent("response.completed", json.RawMessage(`{"type":"response.completed","response":{"id":"r","output":[{"type":"message","content":[]},`+call+`]}}`))
	if !strings.Contains(string(completed), want) || !strings.Contains(string(completed), `{"type":"message","content":[]}`) {
		t.Fatalf("terminal: %s", completed)
	}
	if body := names.restoreResponse(json.RawMessage(`{"id":"r","output":[` + call + `]}`)); !strings.Contains(string(body), want) {
		t.Fatalf("response body: %s", body)
	}
	if args := names.restoreEvent("response.function_call_arguments.done", json.RawMessage(`{"type":"response.function_call_arguments.done","name":"agents-list_agents","arguments":"{}"}`)); !strings.Contains(string(args), `"name":"list_agents"`) {
		t.Fatalf("arguments event: %s", args)
	}
	delta := json.RawMessage(`{"type":"response.output_text.delta","delta":"agents-list_agents"}`)
	if got := names.restoreEvent("response.output_text.delta", delta); string(got) != string(delta) {
		t.Fatalf("text delta changed: %s", got)
	}
	other := json.RawMessage(`{"type":"response.output_item.done","item":{"type":"function_call","name":"exec_command"}}`)
	if got := names.restoreEvent("response.output_item.done", other); string(got) != string(other) {
		t.Fatalf("plain function call changed: %s", got)
	}
}
