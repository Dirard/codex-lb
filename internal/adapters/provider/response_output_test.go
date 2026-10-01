package provider

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestResponseOutputAssembly(t *testing.T) {
	var output responseOutput
	zero, later := 0, 9000000
	for _, value := range []struct {
		index *int
		item  string
	}{
		{&later, `{"id":"old"}`}, {&zero, `{"id":"first","future":true}`},
		{&later, `{"id":"latest"}`}, {nil, `{"id":"unindexed"}`},
	} {
		if err := output.add(value.index, []byte(value.item)); err != nil {
			t.Fatal(err)
		}
	}
	for _, ending := range []string{``, `,"output":null`, `,"output":[]`} {
		body, err := output.fill([]byte(`{"id":"response","usage":{"input_tokens":7},"future":true` + ending + `}`))
		var parsed struct {
			Output []struct{ ID string }
			Usage  struct {
				Input int `json:"input_tokens"`
			}
			Future bool
		}
		if err != nil || json.Unmarshal(body, &parsed) != nil || len(parsed.Output) != 3 || parsed.Output[0].ID != "first" || parsed.Output[1].ID != "latest" || parsed.Output[2].ID != "unindexed" || parsed.Usage.Input != 7 || !parsed.Future {
			t.Fatalf("assembly lost output/metadata: %s %v", body, err)
		}
	}
	canonical := []byte(`{"output":[{"id":"authoritative"}]}`)
	if got, err := output.fill(canonical); err != nil || !bytes.Equal(got, canonical) {
		t.Fatal("nonempty terminal output changed")
	}
	if _, err := output.fill([]byte(`{"output":"malformed"}`)); err == nil {
		t.Fatal("malformed terminal output accepted")
	}
}

func TestResponseOutputBounds(t *testing.T) {
	var output responseOutput
	negative := -1
	for _, item := range []string{`null`, `[]`, `"text"`, `{invalid}`} {
		if output.add(nil, []byte(item)) == nil {
			t.Fatal("invalid item accepted")
		}
	}
	if output.add(&negative, []byte(`{}`)) == nil {
		t.Fatal("negative output index accepted")
	}
	output.retained = maxOperationResponseBytes
	if output.add(nil, []byte(`{}`)) == nil || len(output.unindexed) != 0 {
		t.Fatal("retained-byte bound not enforced")
	}
	if _, err := output.fill(bytes.Repeat([]byte(" "), maxOperationResponseBytes+1)); err == nil {
		t.Fatal("response-byte bound not enforced")
	}
}
