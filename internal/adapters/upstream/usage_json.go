package upstream

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
)

// readUsage distinguishes absent billing from a reported but unusable bill.
// Counter decode errors must not erase an independently decoded quota refusal.
func (r *Result) readUsage(raw json.RawMessage, protocol Protocol) {
	r.UsageReported = len(raw) != 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
	if !r.UsageReported {
		return
	}
	if protocol == ProtocolResponses {
		var usage responsesWireUsage
		if json.Unmarshal(raw, &usage) == nil && usage.known() {
			r.Usage, r.UsageKnown = usage.responseUsage(), true
		}
	} else {
		var usage chatWireUsage
		if json.Unmarshal(raw, &usage) == nil && usage.known() {
			r.Usage, r.UsageKnown = usage.responseUsage(), true
		}
	}
}

func validTokenCounts(input, output, total *uint64) bool {
	return input != nil && output != nil && *input <= math.MaxUint64-*output &&
		(total == nil || *total == *input+*output)
}

// Translated Chat Completions responses use the public Responses usage shape,
// not the internal flat counters used by the adapter's accounting code.
func (u Usage) MarshalJSON() ([]byte, error) {
	if u.InputTokens > math.MaxUint64-u.OutputTokens {
		return nil, errors.New("usage total overflows")
	}
	type details struct {
		Tokens uint64 `json:"cached_tokens"`
	}
	type outputDetails struct {
		Tokens uint64 `json:"reasoning_tokens"`
	}
	var reasoning *outputDetails
	if u.ReasoningTokensKnown {
		reasoning = &outputDetails{u.ReasoningTokens}
	}
	return json.Marshal(struct {
		Input         uint64         `json:"input_tokens"`
		Output        uint64         `json:"output_tokens"`
		Total         uint64         `json:"total_tokens"`
		InputDetails  details        `json:"input_tokens_details"`
		OutputDetails *outputDetails `json:"output_tokens_details,omitempty"`
	}{u.InputTokens, u.OutputTokens, u.InputTokens + u.OutputTokens, details{u.CachedTokens}, reasoning})
}
