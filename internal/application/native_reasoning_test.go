package application

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"codex-lb/internal/domain"
)

func TestNativeReasoningAliasesPreserveControlsAndEnforcePolicy(t *testing.T) {
	low := "low"
	for _, test := range []struct {
		body, want, code string
		forced           *string
	}{
		{`{"thinking":"high","keep":null}`, `"thinking":"low"`, "", &low},
		{`{"reasoning":{"effort":"high","summary":"auto"},"keep":null}`, `"reasoning":{"effort":"low","summary":"auto"}`, "", &low},
		{`{"thinking":{"type":"disabled","effort":"high"},"keep":null}`, `"effort":"high"`, "", nil},
		{`{"thinking":{"enabled":false,"effort":"high"},"keep":null}`, `"effort":"high"`, "", nil},
		{`{"thinking":"enabled","keep":null}`, `"thinking":"enabled"`, "", &low},
		{`{"reasoningEffort":"high"}`, "", "reasoning_effort_not_allowed", nil},
		{`{"reasoningEffort":[]}`, "", "invalid_request", &low},
		{`{"reasoning":{"effort":42}}`, "", "invalid_request", &low},
	} {
		var object map[string]json.RawMessage
		_ = json.Unmarshal([]byte(test.body), &object)
		policy := map[string]json.RawMessage{"model": jsonString("test"), "input": jsonString("")}
		key := domain.APIKey{EnforcedReasoningEffort: test.forced, AllowedReasoningEfforts: []string{"low"}}
		err := applyNativeReasoningAliases(object, policy, key, domain.RuntimeSettings{})
		if test.code != "" {
			var failure *ProxyError
			if !errors.As(err, &failure) || failure.Code != test.code {
				t.Fatalf("alias %s: %v", test.body, err)
			}
			continue
		}
		encoded, _ := json.Marshal(object)
		if err != nil || !strings.Contains(string(encoded), test.want) || !strings.Contains(string(encoded), `"keep":null`) {
			t.Fatalf("native controls changed: %s %v", encoded, err)
		}
	}
}
