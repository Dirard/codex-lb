package application

import (
	"bytes"
	"encoding/json"
	"io"

	"codex-lb/internal/domain"
)

// decodeResponseObject validates with the standard decoder and keeps immutable
// views into the caller's body instead of retaining a second full input copy.
// Duplicate keys and noncanonical casing still require the previous wire
// normalization: policy checks and case-insensitive provider decoders must agree.
func decodeResponseObject(body json.RawMessage) (map[string]json.RawMessage, bool, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil, false, domain.ErrInvalid
	}
	object := make(map[string]json.RawMessage)
	clean := true
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return nil, false, domain.ErrInvalid
		}
		start := decoder.InputOffset()
		var discard discardedJSON
		if err := decoder.Decode(&discard); err != nil {
			return nil, false, domain.ErrInvalid
		}
		value := bytes.TrimSpace(body[start:decoder.InputOffset()])
		if len(value) == 0 || value[0] != ':' {
			return nil, false, domain.ErrInvalid
		}
		value = bytes.TrimSpace(value[1:])
		if _, duplicate := object[key]; duplicate {
			clean = false
		}
		for _, character := range key {
			if character > 127 || character >= 'A' && character <= 'Z' {
				clean = false
				break
			}
		}
		object[key] = value[:len(value):len(value)]
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, false, domain.ErrInvalid
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, false, domain.ErrInvalid
	}
	return object, clean, nil
}
