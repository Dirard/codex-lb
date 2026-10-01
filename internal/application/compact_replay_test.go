package application

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"codex-lb/internal/domain"
)

type compactTestCipher struct{}

func (compactTestCipher) Encrypt(plain []byte) ([]byte, error)     { return plain, nil }
func (compactTestCipher) Decrypt(encrypted []byte) ([]byte, error) { return encrypted, nil }

func TestCompactReplayRequiresCompleteNeutralUntrimmedHistory(t *testing.T) {
	service := &CodexOperations{cipher: compactTestCipher{}}
	stored := []json.RawMessage{
		json.RawMessage(`{"role":"user","content":"original"}`),
		json.RawMessage(`{"type":"custom_tool_call","id":"provider-item","call_id":"tool-1","name":"apply_patch","input":"patch"}`),
	}
	history, _ := json.Marshal(responseContext{Items: stored})
	owner := &domain.Continuation{ContextEncrypted: history}
	ctx := context.Background()
	for _, test := range []struct {
		name, input, fields string
		owner               *domain.Continuation
		want                bool
	}{
		{"new compact", `[{"role":"user","content":"hello"}]`, "", nil, true},
		{"answered delta", `[{"type":"custom_tool_call_output","call_id":"tool-1","output":"done"},{"type":"compaction_trigger"}]`, "", owner, true},
		{"full prefix", `[{"type":"message","content":"original","role":"user"},{"type":"custom_tool_call","status":"completed","call_id":"tool-1","name":"apply_patch","input":"patch"},{"type":"custom_tool_call_output","call_id":"tool-1","output":"done"}]`, "", owner, true},
		{"no stored history delta", `[{"role":"user","content":"only new tail"}]`, "", &domain.Continuation{}, false},
		{"full authoritative", `[{"role":"user","content":"earlier"},{"role":"assistant","content":[{"type":"output_text","text":"answer"}]},{"role":"user","content":"new"}]`, "", &domain.Continuation{}, true},
		{"trigger after answer", `[{"role":"user","content":"earlier"},{"role":"assistant","content":"answer"},{"type":"compaction_trigger"}]`, "", &domain.Continuation{}, true},
		{"fresh developer metadata", `[{"role":"user","content":"earlier"},{"role":"assistant","phase":"final_answer","content":"answer"},{"role":"user","content":"new"},{"role":"developer","internal_chat_message_metadata_passthrough":{"turn_id":"new-turn"},"content":[{"type":"input_text","text":"new instructions"}]}]`, "", &domain.Continuation{}, true},
		{"changed known prefix", `[{"role":"user","content":"different history"},{"role":"assistant","content":"different answer"}]`, "", owner, false},
		{"unanswered calls", `[{"role":"user","content":"new"}]`, "", owner, false},
		{"encrypted summary", `[{"type":"compaction","encrypted_content":"owner-secret"}]`, "", nil, false},
		{"file handle", `[{"role":"user","content":[{"type":"input_file","file_id":"file-a"}]}]`, "", nil, false},
		{"opaque message", `[{"role":"user","content":"new","owner_state":"opaque"}]`, "", nil, false},
		{"container", `[{"role":"user","content":"new","container_id":"cntr"}]`, "", nil, false},
		{"conversation", `[{"role":"user","content":"new"}]`, `,"conversation":"owner-conversation"`, nil, false},
		{"prompt handle", `[{"role":"user","content":"new"}]`, `,"prompt":{"id":"owner-prompt"}`, nil, false},
		{"empty assistant not proof", `[{"role":"user","content":"old"},{"role":"assistant","content":[]},{"role":"user","content":"new"}]`, "", &domain.Continuation{}, false},
		{"metadata owner state", `[{"role":"user","content":"new"}]`, `,"client_metadata":{"opaque_owner_state":"value"}`, nil, false},
		{"direct patch completed without output", `[{"type":"apply_patch_call","call_id":"patch","caller":{"type":"direct"},"operation":{"type":"delete_file","path":"file.txt"}},{"type":"apply_patch_call_output","call_id":"patch","caller":{"type":"direct"},"output":null,"status":"completed"}]`, "", nil, true},
		{"nonlocal caller", `[{"type":"custom_tool_call","call_id":"call","name":"read","input":"x","caller":{"type":"programmatic","tool_id":"owner-tool"}},{"type":"custom_tool_call_output","call_id":"call","output":"ok"}]`, "", nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := json.RawMessage(`{"model":"gpt-6-sol","input":` + test.input + test.fields + `}`)
			wire, err := service.compactReplay(ctx, body, body, test.owner)
			if (err == nil) != test.want {
				t.Fatalf("want replay=%v error=%v wire=%s", test.want, err, wire)
			}
			if err == nil && test.owner == owner && (strings.Count(string(wire), "original") != 1 || strings.Contains(string(wire), "provider-item")) {
				t.Fatalf("lost/duplicated prefix or old item id: %s", wire)
			}
		})
	}
	original := json.RawMessage(`{"model":"gpt-6-sol","input":[{"role":"user","content":"earlier"},{"role":"assistant","content":"answer"},{"role":"user","content":"new"}]}`)
	trimmed := json.RawMessage(`{"model":"gpt-6-sol","input":[{"role":"user","content":"trim marker"},{"role":"assistant","content":"answer"},{"role":"user","content":"new"}]}`)
	if _, err := service.compactReplay(ctx, trimmed, original, &domain.Continuation{}); err == nil {
		t.Fatal("trimmed authoritative resend was treated as complete")
	}
	largeHistory, _ := json.Marshal(responseContext{Items: []json.RawMessage{compactTestMessage("", "user", compactTokenBudget*4)}})
	if _, err := service.compactReplay(ctx, []byte(`{"input":[{"role":"user","content":"new"}]}`), []byte(`{"input":[{"role":"user","content":"new"}]}`), &domain.Continuation{ContextEncrypted: largeHistory}); err == nil {
		t.Fatal("reconstructed context was silently trimmed again")
	}
}
