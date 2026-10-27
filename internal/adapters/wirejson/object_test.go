package wirejson

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestEncodeObjectIsSemanticallyEqualToMarshal(t *testing.T) {
	source := []byte(`{"model":"m","input":[{"type":"message","content":"a <b> & <c>"}],
		"tools": [ {"type":"function","name":"x"} ],"<key>":true,"n":1.50,"nil":null}`)
	var object map[string]json.RawMessage
	if err := json.Unmarshal(source, &object); err != nil {
		t.Fatal(err)
	}
	object["stream"] = json.RawMessage("true")
	object["empty"] = nil
	encoded := EncodeObject(object)
	if !json.Valid(encoded) {
		t.Fatalf("invalid encoding: %s", encoded)
	}
	marshalled, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	var left, right any
	if json.Unmarshal(encoded, &left) != nil || json.Unmarshal(marshalled, &right) != nil || !reflect.DeepEqual(left, right) {
		t.Fatalf("semantic mismatch:\n%s\n%s", encoded, marshalled)
	}
	if !strings.Contains(string(encoded), `"a <b> & <c>"`) || !strings.Contains(string(encoded), `[ {"type":"function","name":"x"} ]`) {
		t.Fatalf("values were re-encoded: %s", encoded)
	}
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	_, _ = decoder.Token()
	previous := ""
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			t.Fatal(err)
		}
		key := token.(string)
		if key < previous {
			t.Fatalf("keys are not sorted: %q after %q", key, previous)
		}
		previous = key
		var skip json.RawMessage
		if err := decoder.Decode(&skip); err != nil {
			t.Fatal(err)
		}
	}
}
