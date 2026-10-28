package upstream

import (
	"encoding/json"
	"errors"
)

// eventHeader is the part of an upstream Responses event needed for routing.
// Event payloads are forwarded verbatim; only this header is decoded per event.
type eventHeader struct {
	Type string `json:"type"`
	// Error is non-nil when the key is present, including an explicit null.
	Error json.RawMessage `json:"error"`
}

// decodeEventHeader validates data as one exact JSON value (no trailing data)
// and decodes its routing header. A non-string type is treated as absent, as a
// generic map decode did, while a non-object event remains invalid.
func decodeEventHeader(data []byte) (eventHeader, error) {
	var header eventHeader
	err := json.Unmarshal(data, &header)
	var mismatch *json.UnmarshalTypeError
	if errors.As(err, &mismatch) && mismatch.Field == "type" {
		header.Type = ""
		return header, nil
	}
	if err != nil {
		return eventHeader{}, &Error{Code: ErrorCodeInvalidStream, Message: "upstream returned invalid JSON"}
	}
	return header, nil
}
