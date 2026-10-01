package application

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

func TestReplayPreservesAnsweredToolsAndDropsOwnerState(t *testing.T) {
	key := domain.APIKey{AllowedModels: []string{"gpt-6-sol"}}
	request, err := parseResponse(json.RawMessage(`{"model":"gpt-6-sol-2026-09-22","previous_response_id":"old","turn_state":"private-owner-state","input":[{"type":"function_call_output","call_id":"call_1","output":"done"}],"text":{"format":{"type":"json_object"}},"metadata":{"keep":"yes"}}`), key, domain.RuntimeSettings{}, false)
	if err != nil {
		t.Fatal(err)
	}
	history := responseContext{Items: []json.RawMessage{
		json.RawMessage(`{"role":"user","content":"request"}`),
		json.RawMessage(`{"type":"reasoning","encrypted_content":"owner-bound"}`),
		json.RawMessage(`{"type":"function_call","id":"old-item-id","call_id":"call_1","name":"tool","arguments":"{}"}`),
	}}
	body, err := replayBody(request, history)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"previous_response_id", "turn_state", "owner-bound", "old-item-id"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("replay kept %s", forbidden)
		}
	}
	for _, required := range []string{"call_1", "function_call_output", "json_object", "metadata"} {
		if !strings.Contains(string(body), required) {
			t.Fatalf("replay lost %s", required)
		}
	}
	if _, err := replayBody(request, responseContext{}); err == nil {
		t.Fatal("orphan tool output replayed")
	}
	history.Items = append(history.Items, json.RawMessage(`{"type":"input_file","file_id":"file_test"}`))
	if _, err := replayBody(request, history); err == nil {
		t.Fatal("account-owned file replayed")
	}
	if _, err := parseResponse(json.RawMessage(`{"model":"gpt-6-solar","input":"hi"}`), key, domain.RuntimeSettings{}, false); err == nil {
		t.Fatal("pricing alias expanded model access")
	}
	if _, err := parseResponse(json.RawMessage(`{"model":"gpt-6-sol","input":null}`), key, domain.RuntimeSettings{}, false); err == nil {
		t.Fatal("null input accepted")
	}
}

func TestSelectionStrategiesAndWeights(t *testing.T) {
	proxy := &Proxy{}
	now := time.Now()
	soon, later := now.Add(time.Hour), now.Add(48*time.Hour)
	choices := func() []accountCandidate {
		return []accountCandidate{
			{account: domain.Account{ID: "a", PlanType: "plus"}, primary: 70, secondary: 20, secondaryKnown: true, secondaryReset: &later},
			{account: domain.Account{ID: "b", PlanType: "pro"}, primary: 10, secondary: 30, secondaryKnown: true, secondaryReset: &soon},
		}
	}
	for strategy, want := range map[string]string{"usage_weighted": "a", "fill_first": "a", "sequential_drain": "a", "reset_drain": "b"} {
		selected, err := proxy.chooseAccount(choices(), domain.RuntimeSettings{RoutingStrategy: strategy, PreferEarlierResetWindow: "secondary"})
		if err != nil || selected.account.ID != want {
			t.Fatalf("%s selected %s: %v", strategy, selected.account.ID, err)
		}
	}
	selected, err := proxy.chooseAccount(choices(), domain.RuntimeSettings{RoutingStrategy: "usage_weighted", PreferEarlierResetAccounts: true, PreferEarlierResetWindow: "secondary"})
	if err != nil || selected.account.ID != "b" {
		t.Fatal("earlier reset preference ignored")
	}
	if weightedIndex([]float64{1, 3}, 0.1) != 0 || weightedIndex([]float64{1, 3}, 0.5) != 1 {
		t.Fatal("capacity weighting incorrect")
	}
	for _, strategy := range []string{"capacity_weighted", "relative_availability", "round_robin", "single_account"} {
		if _, err := proxy.chooseAccount(choices(), domain.RuntimeSettings{RoutingStrategy: strategy, RelativeAvailabilityPower: 2, RelativeAvailabilityTopK: 5}); err != nil {
			t.Fatal(err)
		}
	}
}
