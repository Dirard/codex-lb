// Package wirejson re-assembles provider JSON objects whose values were already
// validated, without re-encoding them.
package wirejson

import (
	"encoding/json"
	"slices"
)

// EncodeObject writes object with sorted keys and its values verbatim. Every
// value must be valid JSON, as produced by json.Unmarshal into a
// map[string]json.RawMessage or by json.Marshal. Unlike json.Marshal it does
// not re-compact or HTML-escape the (possibly megabyte-sized) values, so the
// encoding is semantically identical and costs one copy.
func EncodeObject(object map[string]json.RawMessage) json.RawMessage {
	keys := make([]string, 0, len(object))
	size := 2
	for key, value := range object {
		keys = append(keys, key)
		size += len(key) + len(value) + 4
	}
	slices.Sort(keys)
	out := make([]byte, 0, size)
	out = append(out, '{')
	for i, key := range keys {
		if i > 0 {
			out = append(out, ',')
		}
		quoted, _ := json.Marshal(key) // a Go string always encodes
		out = append(out, quoted...)
		out = append(out, ':')
		value := object[key]
		if len(value) == 0 {
			value = json.RawMessage("null")
		}
		out = append(out, value...)
	}
	return append(out, '}')
}
