package httpapi

import (
	"encoding/json"
	"testing"
)

// NET-20: a field removed by capability preparation must not reappear from the
// original frame, and an unchanged payload is not decoded again.
func TestWebSocketCreateBodyUsesPreparedPayload(t *testing.T) {
	original := json.RawMessage(`{"type":"response.create","model":"m","input":"hi","client_metadata":{"marker":"x"},"note":"a <b>"}`)
	var frame map[string]json.RawMessage
	if err := json.Unmarshal(original, &frame); err != nil {
		t.Fatal(err)
	}
	prepared := json.RawMessage(`{"type":"response.create","model":"m","input":"hi","note":"a <b>"}`)
	body, err := websocketCreateBody(frame, original, prepared)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(body, &object); err != nil {
		t.Fatal(err)
	}
	if _, resurrected := object["client_metadata"]; resurrected || string(object["stream"]) != "true" || object["type"] != nil || string(object["note"]) != `"a <b>"` {
		t.Fatalf("prepared payload not honoured: %s", body)
	}
	frame = map[string]json.RawMessage{"model": json.RawMessage(`"m"`), "type": json.RawMessage(`"response.create"`)}
	body, err = websocketCreateBody(frame, original, original)
	if err != nil || string(body) != `{"model":"m","stream":true}` {
		t.Fatalf("unchanged payload was decoded again: %s %v", body, err)
	}
}
