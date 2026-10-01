package application

import (
	"encoding/json"
	"testing"
)

func TestChatCompletionsToResponsesPreservesContract(t *testing.T) {
	body := nativeMustJSON(map[string]any{
		"model": "public-model", "stream": true,
		"instructions": "system", "messages": []any{
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "hello"},
				map[string]any{"type": "image_url", "image_url": "data:image/png;base64,AAA"},
			}},
			map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
				"id": "call_1", "type": "function",
				"function": map[string]any{"name": "shell", "arguments": "{\"cmd\":\"ls\"}"},
			}}},
			map[string]any{"role": "tool", "tool_call_id": "call_1", "content": "ok"},
		},
		"tools": []any{map[string]any{"type": "function", "function": map[string]any{
			"name": "shell", "parameters": map[string]string{"type": "object"}, "strict": true,
		}}},
		"tool_choice": map[string]any{"type": "function", "function": map[string]string{"name": "shell"}},
		"response_format": map[string]any{"type": "json_schema", "json_schema": map[string]any{
			"name": "output", "schema": map[string]string{"type": "object"}, "strict": true,
		}},
		"max_completion_tokens": 123, "service_tier": "auto", "future": nil,
	})
	responses, err := ChatCompletionsToResponses(body)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]json.RawMessage
	if json.Unmarshal(responses, &payload) != nil || string(payload["store"]) != "false" ||
		payload["messages"] != nil || payload["tool_calls"] != nil || payload["max_completion_tokens"] != nil {
		t.Fatalf("Chat-only fields were not converted: %s", responses)
	}
	if _, ok := payload["future"]; !ok || string(payload["future"]) != "null" {
		t.Fatal("explicit null extension was lost")
	}
	var input []map[string]json.RawMessage
	if json.Unmarshal(payload["input"], &input) != nil || len(input) != 3 ||
		string(input[0]["type"]) != `"message"` || string(input[1]["type"]) != `"function_call"` ||
		string(input[2]["type"]) != `"function_call_output"` {
		t.Fatalf("messages were not converted: %s", payload["input"])
	}
	var tools []map[string]json.RawMessage
	if json.Unmarshal(payload["tools"], &tools) != nil || len(tools) != 1 || string(tools[0]["name"]) != `"shell"` {
		t.Fatalf("tools were not flattened: %s", payload["tools"])
	}
	var choice struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if json.Unmarshal(payload["tool_choice"], &choice) != nil || choice.Type != "function" || choice.Name != "shell" {
		t.Fatalf("tool_choice was not converted: %s", payload["tool_choice"])
	}
	var text struct {
		Format struct {
			Type string `json:"type"`
		} `json:"format"`
	}
	if json.Unmarshal(payload["text"], &text) != nil || text.Format.Type != "json_schema" {
		t.Fatalf("response_format was not converted: %s", payload["text"])
	}
}

func TestResponsesToChatCompletion(t *testing.T) {
	response := nativeMustJSON(map[string]any{
		"id": "resp_1", "model": "public-model",
		"output": []any{
			map[string]any{"type": "message", "role": "assistant", "content": []any{
				map[string]any{"type": "output_text", "text": "hello"},
			}},
			map[string]any{"type": "function_call", "call_id": "call_1", "name": "shell", "arguments": "{}"},
		},
		"usage": map[string]any{
			"input_tokens": 2, "output_tokens": 3,
			"input_tokens_details":  map[string]int{"cached_tokens": 1},
			"output_tokens_details": map[string]int{"reasoning_tokens": 1},
		},
	})
	chat, err := ResponsesToChatCompletion(response)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name string `json:"name"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(chat, &payload) != nil || len(payload.Choices) != 1 ||
		payload.Choices[0].FinishReason != "tool_calls" || payload.Choices[0].Message.ToolCalls[0].ID != "call_1" ||
		payload.Usage.PromptTokens != 2 || payload.Usage.CompletionTokens != 3 {
		t.Fatalf("Responses payload was not converted: %s", chat)
	}
}
