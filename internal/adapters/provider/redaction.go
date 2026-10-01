package provider

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

// redactCredential removes a known provider credential while preserving JSON
// number lexemes. json.Number is intentionally used instead of float64 so
// provider-specific large or high-precision numbers are not re-encoded.
func redactCredential(body []byte, credential string) []byte {
	credential = strings.TrimSpace(credential)
	if credential == "" {
		return body
	}
	if !json.Valid(body) {
		return bytes.ReplaceAll(body, []byte(credential), []byte("[redacted]"))
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil || decoder.Decode(new(any)) != io.EOF {
		return bytes.ReplaceAll(body, []byte(credential), []byte("[redacted]"))
	}
	redactCredentialValue(value, credential)
	encoded, err := json.Marshal(value)
	if err != nil {
		return bytes.ReplaceAll(body, []byte(credential), []byte("[redacted]"))
	}
	return encoded
}

func redactCredentialValue(value any, credential string) {
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			if text, ok := child.(string); ok && strings.Contains(text, credential) {
				value[key] = "[redacted]"
				continue
			}
			redactCredentialValue(child, credential)
		}
	case []any:
		for _, child := range value {
			redactCredentialValue(child, credential)
		}
	}
}
