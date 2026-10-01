package provider

import (
	"bytes"
	"encoding/json"
	"sort"
)

// responseOutput retains bounded final items, not a stream's repeated deltas.
type responseOutput struct {
	indexed   map[int]json.RawMessage
	unindexed []json.RawMessage
	retained  int
}

func (o *responseOutput) add(index *int, item json.RawMessage) error {
	item = bytes.TrimSpace(item)
	if len(item) == 0 || item[0] != '{' || !json.Valid(item) || index != nil && *index < 0 {
		return providerFailure("invalid_upstream_response", 502, true)
	}
	retained := o.retained + len(item)
	if index != nil {
		retained -= len(o.indexed[*index])
	}
	if retained > maxOperationResponseBytes {
		return providerFailure("upstream_response_too_large", 502, true)
	}
	if index == nil {
		o.unindexed = append(o.unindexed, item)
	} else {
		if o.indexed == nil {
			o.indexed = make(map[int]json.RawMessage)
		}
		o.indexed[*index] = item
	}
	o.retained = retained
	return nil
}

// fill assembles missing terminal output in index order without changing billing.
func (o *responseOutput) fill(body json.RawMessage) (json.RawMessage, error) {
	if len(body) > maxOperationResponseBytes {
		return nil, providerFailure("upstream_response_too_large", 502, true)
	}
	var terminal map[string]json.RawMessage
	if json.Unmarshal(body, &terminal) != nil || terminal == nil {
		return nil, providerFailure("invalid_upstream_response", 502, true)
	}
	var output []json.RawMessage
	if raw := terminal["output"]; len(raw) != 0 && json.Unmarshal(raw, &output) != nil {
		return nil, providerFailure("invalid_upstream_response", 502, true)
	}
	if len(output) != 0 || o.retained == 0 {
		return body, nil
	}
	indices := make([]int, 0, len(o.indexed))
	for index := range o.indexed {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, index := range indices {
		output = append(output, o.indexed[index])
	}
	output = append(output, o.unindexed...)
	terminal["output"], _ = json.Marshal(output)
	assembled, err := json.Marshal(terminal)
	if err != nil {
		return nil, providerFailure("invalid_upstream_response", 502, true)
	}
	if len(assembled) > maxOperationResponseBytes {
		return nil, providerFailure("upstream_response_too_large", 502, true)
	}
	return assembled, nil
}
