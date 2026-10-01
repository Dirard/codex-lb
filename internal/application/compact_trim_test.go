package application

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"codex-lb/internal/domain"
)

func compactTestItem(fields map[string]any) json.RawMessage {
	raw, err := json.Marshal(fields)
	if err != nil {
		panic(err)
	}
	return raw
}

func TestCompactScalarInputCannotBypassHistoryBudget(t *testing.T) {
	for _, size := range []int{20, compactTokenBudget * 4} {
		body := compactTestItem(map[string]any{"model": "gpt-6-sol", "input": strings.Repeat("x", size), "max_output_tokens": "ignored-by-compact"})
		prepared, _, err := prepareCompact(context.Background(), body, domain.APIKey{}, domain.RuntimeSettings{}, false)
		if size == 20 {
			var object map[string]json.RawMessage
			var input []json.RawMessage
			if err != nil || json.Unmarshal(prepared, &object) != nil || json.Unmarshal(object["input"], &input) != nil || len(input) != 1 || object["max_output_tokens"] != nil {
				t.Fatalf("scalar normalization failed: %s %v", prepared, err)
			}
		} else if err == nil {
			t.Fatal("oversized scalar compact input bypassed the mandatory-tail budget")
		}
	}
}

func compactTestMessage(id, role string, size int) json.RawMessage {
	return compactTestItem(map[string]any{"id": id, "role": role, "content": strings.Repeat("x", size)})
}

func compactTestPair(kind, name, callID string, size int) (json.RawMessage, json.RawMessage) {
	field := "arguments"
	if kind == "custom_tool_call" {
		field = "input"
	}
	return compactTestItem(map[string]any{"type": kind, "name": name, "call_id": callID, field: "{}"}),
		compactTestItem(map[string]any{"type": kind + "_output", "call_id": callID, "output": strings.Repeat("y", size)})
}

func containsCompactItem(input []json.RawMessage, expected json.RawMessage) bool {
	for _, raw := range input {
		if string(raw) == string(expected) {
			return true
		}
	}
	return false
}

func checkedCompactTrim(t *testing.T, input []json.RawMessage, anchored bool) []json.RawMessage {
	t.Helper()
	result, err := trimCompactHistory(context.Background(), input, anchored)
	if err != nil {
		t.Fatal(err)
	}
	if size := compactInputSize(result); size > 4*compactTokenBudget {
		t.Fatalf("wire size %d exceeds budget", size)
	}
	second, err := trimCompactHistory(context.Background(), result, anchored)
	if err != nil || !reflect.DeepEqual(result, second) {
		t.Fatal("prepared compact history is not stable")
	}
	return result
}

func TestCompactHistoryPreservesFittingRawItemsAndMarksOmittedRanges(t *testing.T) {
	input := []json.RawMessage{json.RawMessage(`{"counter":9007199254740993, "content":"literal <tag> and ё"}`), json.RawMessage(`null`)}
	if got := checkedCompactTrim(t, input, false); !reflect.DeepEqual(got, input) {
		t.Fatal("fitting raw input changed")
	}
	head, middle, latest := compactTestMessage("head", "user", 20), compactTestMessage("middle", "assistant", 500_000), compactTestMessage("latest", "user", 20)
	got := checkedCompactTrim(t, []json.RawMessage{head, middle, latest}, false)
	if len(got) != 3 || string(got[0]) != string(head) || string(got[2]) != string(latest) || !strings.Contains(string(got[1]), "Omitted 1 input items") {
		t.Fatalf("wrong retained order/marker: %d", len(got))
	}
	h, err := newCompactHistory(context.Background(), []json.RawMessage{head, middle, latest})
	if err != nil {
		t.Fatal(err)
	}
	if measured := h.selectedSize(map[int]bool{0: true, 2: true}); measured != compactInputSize(got) {
		t.Fatalf("marker estimator=%d, wire=%d", measured, compactInputSize(got))
	}
}

func TestCompactHistoryRetainsStateAndOnlyTheRequiredToolOccurrence(t *testing.T) {
	tools := compactTestItem(map[string]any{"type": "additional_tools", "tools": []any{map[string]any{"name": "synthetic-tool"}}})
	developer := compactTestMessage("directive", "developer", 60_000)
	goal := compactTestItem(map[string]any{"role": "user", "content": `<codex_internal_context source="goal">keep this state`})
	oldCall, oldOutput := compactTestPair("function_call", "read", "reused", 500_000)
	stateCall, stateOutput := compactTestPair("function_call", "update_plan", "reused", 30)
	latest := compactTestMessage("latest", "user", 20)
	input := []json.RawMessage{tools, developer, oldCall, oldOutput, stateCall, stateOutput, goal, latest}
	got := checkedCompactTrim(t, input, false)
	for _, required := range []json.RawMessage{tools, developer, stateCall, stateOutput, goal, latest} {
		if !containsCompactItem(got, required) {
			t.Fatalf("required state disappeared: %.100s", required)
		}
	}
	if containsCompactItem(got, oldCall) || containsCompactItem(got, oldOutput) {
		t.Fatal("reused ID retained an unrelated occurrence")
	}
}

func TestCompactHistoryPrioritizesCompleteHistoricalSideEffects(t *testing.T) {
	for _, name := range []string{"exec", "collaboration", "functions.exec", "exec_command"} {
		t.Run(name, func(t *testing.T) {
			call, output := compactTestPair("custom_tool_call", name, "side-effect", 20)
			ordinary := compactTestMessage("ordinary", "assistant", 300_000)
			input := []json.RawMessage{compactTestMessage("required", "developer", 150_000), compactTestMessage("prefix", "assistant", 260_000), call, output, ordinary, compactTestMessage("latest", "user", 20)}
			got := checkedCompactTrim(t, input, false)
			if !containsCompactItem(got, call) || !containsCompactItem(got, output) || containsCompactItem(got, ordinary) {
				t.Fatal("side-effect pair lost its priority over ordinary context")
			}
		})
	}
	call, output := compactTestPair("custom_tool_call", "exec", "side-effect", 150_000)
	directive, latest := compactTestMessage("directive", "developer", 300_000), compactTestMessage("latest", "user", 20)
	got := checkedCompactTrim(t, []json.RawMessage{directive, call, output, latest}, false)
	if containsCompactItem(got, call) || containsCompactItem(got, output) || !containsCompactItem(got, directive) {
		t.Fatal("optional side effect displaced required state or became unpaired")
	}
	orphan := compactTestItem(map[string]any{"type": "custom_tool_call", "name": "exec", "input": "{}"})
	got = checkedCompactTrim(t, []json.RawMessage{compactTestMessage("filler", "assistant", 500_000), orphan, latest}, false)
	if containsCompactItem(got, orphan) {
		t.Fatal("unverifiable historical side effect retained")
	}
}

func TestCompactHistoryRejectsUntrimmableRequiredTails(t *testing.T) {
	call, output := compactTestPair("custom_tool_call", "exec", "side-effect", 500_000)
	wrongCall, _ := compactTestPair("function_call", "read", "wrong-variant", 0)
	_, unmatched := compactTestPair("custom_tool_call", "read", "wrong-variant", 500_000)
	consumedCall, consumedOutput := compactTestPair("function_call", "read", "consumed", 10)
	_, consumedTail := compactTestPair("function_call", "read", "consumed", 500_000)
	for _, tc := range []struct {
		name     string
		input    []json.RawMessage
		anchored bool
	}{
		{"latest message", []json.RawMessage{compactTestMessage("latest", "user", 500_000)}, false},
		{"side effect", []json.RawMessage{call, output}, false},
		{"wrong variant anchor", []json.RawMessage{wrongCall, unmatched}, true},
		{"consumed occurrence anchor", []json.RawMessage{consumedCall, consumedOutput, consumedTail}, true},
		{"unmatched call", []json.RawMessage{compactTestItem(map[string]any{"type": "function_call", "name": "read", "call_id": "pending", "arguments": strings.Repeat("x", 500_000)})}, false},
		{"unobserved image", []json.RawMessage{compactTestItem(map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_image", "image_url": "data:image/png;base64," + strings.Repeat("A", 500_000)}}})}, false},
		{"hosted screenshot", []json.RawMessage{compactTestItem(map[string]any{"type": "computer_call_output", "call_id": "computer", "output": map[string]any{"type": "input_image", "image_url": "data:image/png;base64," + strings.Repeat("A", 500_000)}})}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := trimCompactHistory(context.Background(), tc.input, tc.anchored)
			if err == nil || !strings.Contains(err.Error(), "cannot be trimmed") {
				t.Fatalf("required state was lost: %v", err)
			}
		})
	}
}

func TestCompactHistoryFitsOrdinaryPairsAndElidesImagesOnlyAsNeeded(t *testing.T) {
	trigger := json.RawMessage(`{"type":"compaction_trigger"}`)
	for _, kind := range []string{"function_call", "custom_tool_call"} {
		call, output := compactTestPair(kind, "read", "ordinary", 500_000)
		got := checkedCompactTrim(t, []json.RawMessage{compactTestMessage("directive", "developer", 30_000), call, output, trigger}, true)
		if containsCompactItem(got, call) || containsCompactItem(got, output) || string(got[len(got)-1]) != string(trigger) {
			t.Fatal("ordinary oversized pair split or trigger lost")
		}
	}
	call, _ := compactTestPair("function_call", "render", "image", 0)
	for _, imageSize := range []int{300_000, 500_000} {
		output := compactTestItem(map[string]any{"type": "function_call_output", "call_id": "image", "output": "data:image/png;base64," + strings.Repeat("A", imageSize), "counter": uint64(9007199254740993)})
		got := checkedCompactTrim(t, []json.RawMessage{compactTestMessage("filler", "user", 250_000), call, output}, false)
		encoded, _ := json.Marshal(got)
		elided := strings.Contains(string(encoded), "Omitted inline image bytes")
		if elided != (imageSize == 500_000) || !strings.Contains(string(encoded), "9007199254740993") {
			t.Fatal("lossless preference or integer preservation changed")
		}
	}
}

func TestCompactHistoryCountsFramingAndSupportsCancellation(t *testing.T) {
	input := make([]json.RawMessage, 14_285)
	for i := range input {
		input[i] = json.RawMessage(`{"role":"user","content":""}`)
	}
	got := checkedCompactTrim(t, input, false)
	if len(got) >= len(input) {
		t.Fatal("many-item framing did not trigger trimming")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := trimCompactHistory(ctx, input, false); err != context.Canceled {
		t.Fatalf("cancellation ignored: %v", err)
	}
}

func TestCompactHistoryMarkerSizeMatchesActualWireForEverySelection(t *testing.T) {
	input := []json.RawMessage{
		json.RawMessage(`{"role":"user","content":"ё 😀 <tag> and \\"}`),
		json.RawMessage(`{"counter":9007199254740993,"content":["text","a,b:c"]}`),
		json.RawMessage(`null`), json.RawMessage(`[1,2,3]`),
		compactTestMessage("last", "user", 37),
	}
	h, err := newCompactHistory(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	for bits := 0; bits < 1<<len(input); bits++ {
		selected := map[int]bool{}
		for i := range input {
			if bits&(1<<i) != 0 {
				selected[i] = true
			}
		}
		if size, actual := h.selectedSize(selected), compactInputSize(h.withMarkers(selected)); size != actual {
			t.Fatalf("selection %b: estimated=%d actual=%d", bits, size, actual)
		}
	}
}

func FuzzCompactHistoryKeepsTypedPairsAndWireBudget(f *testing.F) {
	f.Add([]byte{0, 5, 2, 3, 4, 5})
	f.Add([]byte{1, 3, 3, 3, 3, 3})
	f.Add([]byte{2, 4, 5, 1, 2, 3})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) == 0 {
			return
		}
		kind := []string{"function_call", "custom_tool_call", "apply_patch_call"}[int(data[0])%3]
		input := []json.RawMessage{compactTestMessage("instructions", "developer", 40)}
		var pairs [][2]json.RawMessage
		for i, size := range data[1:min(7, len(data))] {
			call, output := compactTestPair(kind, "read", fmt.Sprintf("call-%d", i%2), int(size%6)*50_000)
			input = append(input, compactTestMessage(fmt.Sprintf("filler-%d", i), "assistant", int(size%5)*30_000), call, output)
			pairs = append(pairs, [2]json.RawMessage{call, output})
		}
		input = append(input, compactTestMessage("latest", "user", 30))
		got, err := trimCompactHistory(context.Background(), input, data[0]&1 != 0)
		if err != nil {
			if !strings.Contains(err.Error(), "responses_compact_input_too_large") {
				t.Fatal(err)
			}
			return
		}
		if compactInputSize(got) > compactTokenBudget*4 || !containsCompactItem(got, input[0]) || !containsCompactItem(got, input[len(input)-1]) {
			t.Fatal("budget or mandatory state changed")
		}
		// Repeated call IDs are permitted, so compare retained occurrence counts.
		for _, pair := range pairs {
			calls, outputs := 0, 0
			callID := compactStringField(pair[0], "call_id")
			for _, raw := range got {
				if compactStringField(raw, "call_id") == callID {
					switch compactStringField(raw, "type") {
					case kind:
						calls++
					case kind + "_output":
						outputs++
					}
				}
			}
			if calls != outputs {
				t.Fatal("typed occurrence pair was split")
			}
		}
	})
}

func compactStringField(raw json.RawMessage, field string) string {
	var object map[string]json.RawMessage
	_ = json.Unmarshal(raw, &object)
	return compactString(object[field])
}
