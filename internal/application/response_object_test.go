package application

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"codex-lb/internal/domain"
)

func TestResponseObjectSharesInputWithoutMutatingCaller(t *testing.T) {
	body := []byte(` { "model" : "gpt-6-sol", "input" : "` + strings.Repeat("x", 1<<20) + `\u2603", "stream":true } `)
	original := bytes.Clone(body)
	request, err := parseResponse(body, domain.APIKey{}, domain.RuntimeSettings{}, false)
	if err != nil || !request.WireClean || !request.DeferredInput || len(request.Input) != 0 {
		t.Fatalf("plain input was copied or changed: %v", err)
	}
	input := request.Object["input"]
	start := bytes.Index(body, []byte(`"xxx`))
	if &input[0] != &body[start] || cap(input) != len(input) {
		t.Fatal("input is not a bounded view into the caller body")
	}
	request.Object["input"] = append(input, ' ')
	if !bytes.Equal(body, original) {
		t.Fatal("extending a field mutated caller bytes")
	}
}

func TestResponseInputStillValidatesEveryItem(t *testing.T) {
	for _, valid := range []string{`[]`, ` [ {}, {"content":[{"type":"input_text","text":"雪"}]} ] `, `"plain text"`} {
		if _, err := responseInput([]byte(valid)); err != nil {
			t.Fatalf("rejected valid input %s: %v", valid, err)
		}
	}
	for _, invalid := range []string{`[null]`, `["text"]`, `[[]]`, `[1]`, `[{},null]`, `[{}] true`, `[{"content":]`, `"unterminated`, `null`} {
		if _, err := responseInput([]byte(invalid)); err == nil {
			t.Fatalf("accepted invalid input %s", invalid)
		}
	}
}

func FuzzDecodeResponseObject(f *testing.F) {
	for _, seed := range []string{
		`{}`, `null`, `[]`, `{"input":"hi"} {}`, `{"input":"bad\u"}`,
		" { \"input\" \n:\t [1, {\"x\":\"雪☃\\n\"}], \"empty\":{} } ",
		`{"model":"first","m\u006fdel":"last","Model":"alias"}`,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		var expected map[string]json.RawMessage
		wantErr := json.Unmarshal([]byte(value), &expected)
		actual, _, err := decodeResponseObject([]byte(value))
		if wantErr != nil || expected == nil {
			if err == nil {
				t.Fatal("accepted non-object or malformed JSON")
			}
			return
		}
		if err != nil {
			t.Fatalf("rejected valid object: %v", err)
		}
		want, _ := json.Marshal(expected)
		got, _ := json.Marshal(actual)
		if !bytes.Equal(got, want) {
			t.Fatalf("changed JSON semantics: %s != %s", got, want)
		}
	})
}
