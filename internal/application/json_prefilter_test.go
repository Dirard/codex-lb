package application

import (
	"encoding/json"
	"slices"
	"testing"

	"codex-lb/internal/domain"
)

var prefilterSeeds = []string{
	`{"role":"user","content":"hello"}`,
	`{"type":"input_file","file_id":"file-abc"}`,
	`{"type":"message","content":[{"type":"input_text","text":"file-abc"}]}`,
	`{"file_ids":["file_1","file_2"]}`,
	`{"tool":{"container_id":"cntr"}}`,
	`{"vector_store_ids":["vs"]}`,
	`{"type":"input_audio","call_id":"call"}`,
	`{"type":"item_reference","id":"msg"}`,
	`{"type":"compaction","encrypted_content":"x"}`,
	`{"type":"compaction_trigger"}`,
	`{"type":"additional_tools","role":"developer","tools":[{"type":"function","name":"f","parameters":{}}]}`,
	`{"type":"additional_tools","role":"developer","tools":[{"type":"web_search"}]}`,
	`{"file_id":"x"}`,
	`{"type":"compaction"}`,
	`{"text":"file-abc"}`,
	`{"type":"item_reference"}`,
	`{"type":"input_image","image_url":"data:"}`,
	`{"type":"input_image","image_url":"https://example.invalid/x.png"}`,
	`{"type":"image_generation"}`,
	`{"content":[{"type":"input_text","text":"no images here"}]}`,
	`{"a":1} {"type":"input_image"}`,
	`{"a":1} trailing`,
	`{"unterminated":`,
	``,
	`"file-scalar"`,
	`["file_x", {"nested":{"file_id":7}}]`,
	"{\"text\":\"file\xff-x\"}",
	`{"n":1e1000}`,
}

func FuzzAccountBoundPrefilterMatchesDecode(f *testing.F) {
	for _, seed := range prefilterSeeds {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if got, want := accountBoundItem(raw), accountBoundDecoded(raw); got != want {
			t.Fatalf("prefilter %v, decode %v for %q", got, want, raw)
		}
	})
}

func FuzzFileIDPrefilterMatchesDecode(f *testing.F) {
	for _, seed := range prefilterSeeds {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		got, want := collectFileIDs(raw), collectFileIDsDecoded(raw)
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatalf("prefilter %q, decode %q for %q", got, want, raw)
		}
	})
}

func FuzzTransportPrefilterMatchesDecode(f *testing.F) {
	for _, seed := range prefilterSeeds {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if got, want := autoTransportRequiresHTTP(raw), transportRequiresHTTPDecoded(raw); got != want {
			t.Fatalf("prefilter %v, decode %v for %q", got, want, raw)
		}
	})
}

// APP-14: the stored owner flag covers its history, so the next owner needs
// only the new output scanned.
func TestContinuationPinIsIncremental(t *testing.T) {
	history := []json.RawMessage{json.RawMessage(`{"type":"input_file","file_id":"file-a"}`)}
	if !accountBoundItems(history) {
		t.Fatal("fixture must be account-bound")
	}
	proxy := &Proxy{config: ProxyConfig{ContinuationTTL: 60_000_000_000}, cipher: compactTestCipher{}}
	request := responseRequest{Previous: "resp_prev", Model: "gpt-6-sol", Input: []json.RawMessage{json.RawMessage(`{"role":"user","content":"next"}`)}}
	previous := &domain.Continuation{ResponseID: "resp_prev", FilePinned: true, ContextEncrypted: []byte("stored")}
	testAccount := domain.Account{ID: "account-a", Provider: "openai"}
	result := ResponseResult{ResponseID: "resp_next", Response: json.RawMessage(`{"output":[{"type":"message","role":"assistant","content":"ok"}]}`)}
	owner, _, err := proxy.continuationPlan(ResponseOptions{KeyID: "key"}, request, responseContext{Items: history}, true, previous, testAccount, result, false, true)
	if err != nil || !owner.FilePinned {
		t.Fatalf("pin from previous owner lost: %+v %v", owner, err)
	}
	output := ResponseResult{ResponseID: "resp_out", Response: json.RawMessage(`{"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"see file-b"}]},{"type":"input_file","file_id":"file-b"}]}`)}
	fresh, _, err := proxy.continuationPlan(ResponseOptions{KeyID: "key"}, responseRequest{Model: "gpt-6-sol"}, responseContext{}, true, nil, testAccount, output, false, true)
	if err != nil || !fresh.FilePinned {
		t.Fatalf("account-bound output not pinned: %+v %v", fresh, err)
	}
}
