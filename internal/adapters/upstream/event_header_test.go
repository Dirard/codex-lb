package upstream

import (
	"errors"
	"testing"
)

func TestDecodeEventHeaderKeepsExactJSONContract(t *testing.T) {
	for _, test := range []struct {
		data     string
		valid    bool
		typeName string
		hasError bool
	}{
		{`{"type":"response.output_text.delta","delta":"x"}`, true, "response.output_text.delta", false},
		{`{"type":"error","error":{"code":"x"}}`, true, "error", true},
		{`{"type":"response.created","error":null}`, true, "response.created", true},
		{`{"type":7,"delta":"x"}`, true, "", false},
		{`null`, true, "", false},
		{`{"type":"a"} {"type":"b"}`, false, "", false},
		{`{"type":"a"`, false, "", false},
		{`[1]`, false, "", false},
		{`"text"`, false, "", false},
	} {
		header, err := decodeEventHeader([]byte(test.data))
		var failure *Error
		if test.valid != (err == nil) || err != nil && (!errors.As(err, &failure) || failure.Code != ErrorCodeInvalidStream) {
			t.Fatalf("%s: err=%v", test.data, err)
		}
		if header.Type != test.typeName || (header.Error != nil) != test.hasError {
			t.Fatalf("%s: header=%+v", test.data, header)
		}
	}
}
