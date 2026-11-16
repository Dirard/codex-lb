package domain

import (
	"testing"
	"time"
)

func ptr[T any](v T) *T { return &v }

func TestForkViolationNamesTheWiderSetting(t *testing.T) {
	soon, later := time.Now().Add(time.Hour), time.Now().Add(2*time.Hour)
	parent := APIKey{AllowedModels: []string{"sol", "luna"}, AllowedReasoningEfforts: []string{"low", "medium"},
		EnforcedServiceTier: ptr("flex"), ExpiresAt: &soon, RequestPool: &RequestPool{RequestsPerMinute: 10, MaxWaitSeconds: 20}}
	fits := APIKey{AllowedModels: []string{"luna"}, AllowedReasoningEfforts: []string{"low"}, EnforcedModel: ptr("luna"),
		RequestPool: &RequestPool{RequestsPerMinute: 10, MaxWaitSeconds: 20}}
	if field := ForkViolation(parent, fits); field != "" {
		t.Fatalf("a narrower fork exceeds at %s", field)
	}
	for field, fork := range map[string]APIKey{
		"allowedModels":           {AllowedModels: nil, AllowedReasoningEfforts: []string{"low"}},
		"enforcedModel":           {AllowedModels: []string{"luna"}, EnforcedModel: ptr("sol"), AllowedReasoningEfforts: []string{"low"}},
		"allowedReasoningEfforts": {AllowedModels: []string{"luna"}, AllowedReasoningEfforts: []string{"low", "high"}},
		"enforcedReasoningEffort": {AllowedModels: []string{"luna"}, EnforcedReasoningEffort: ptr("high")},
		"enforcedServiceTier":     {AllowedModels: []string{"luna"}, AllowedReasoningEfforts: []string{"low"}, EnforcedServiceTier: ptr("priority")},
		"expiresAt":               {AllowedModels: []string{"luna"}, AllowedReasoningEfforts: []string{"low"}, ExpiresAt: &later},
		"requestPool":             {AllowedModels: []string{"luna"}, AllowedReasoningEfforts: []string{"low"}, RequestPool: &RequestPool{RequestsPerMinute: 11, MaxWaitSeconds: 20}},
	} {
		if got := ForkViolation(parent, fork); got != field {
			t.Errorf("%s: got %q", field, got)
		}
	}
	// Enforcement on the parent leaves the fork nothing to choose.
	enforcing := APIKey{EnforcedModel: ptr("sol"), EnforcedReasoningEffort: ptr("high")}
	if got := ForkViolation(enforcing, APIKey{AllowedReasoningEfforts: []string{"high"}}); got != "allowedReasoningEfforts" {
		t.Fatalf("effort list under an enforced effort: %q", got)
	}
	if got := ForkViolation(enforcing, APIKey{EnforcedModel: ptr("luna")}); got != "enforcedModel" {
		t.Fatalf("other enforced model: %q", got)
	}
	if got := ForkViolation(enforcing, APIKey{}); got != "" {
		t.Fatalf("fork inheriting enforcement: %q", got)
	}
	// A parent without restrictions admits any fork; its limits stay free.
	if got := ForkViolation(APIKey{}, APIKey{AllowedModels: []string{"x"}, EnforcedServiceTier: ptr("priority"), RequestPool: &RequestPool{RequestsPerMinute: 5}}); got != "" {
		t.Fatalf("unrestricted parent: %q", got)
	}
}

func TestInheritFromParentServesTheForkWithinItsParent(t *testing.T) {
	soon, later := time.Now().Add(time.Hour), time.Now().Add(2*time.Hour)
	group := "group"
	parent := APIKey{ID: "parent", Name: "Parent", IsActive: true, ExpiresAt: &soon, GroupID: &group, AccountAssignmentScopeEnabled: true,
		AssignedAccountIDs: []string{"a"}, TrafficClass: "opportunistic", UsageSections: "upstream_limits", EnforcedModel: ptr("sol"),
		ApplyToCodexModel: true, EnforcedServiceTier: ptr("flex"), RequestPool: &RequestPool{RequestsPerMinute: 9}}
	fork := APIKey{ID: "fork", IsActive: true, ExpiresAt: &later, AllowedModels: []string{"sol"}, TrafficClass: "foreground",
		RequestPool: &RequestPool{RequestsPerMinute: 3}, Fork: &KeyFork{ParentID: "parent"}}
	got := InheritFromParent(fork, parent)
	if !got.IsActive || !got.ExpiresAt.Equal(soon) || got.GroupID == nil || *got.GroupID != group || !got.AccountAssignmentScopeEnabled ||
		got.TrafficClass != "opportunistic" || got.UsageSections != "upstream_limits" || *got.EnforcedModel != "sol" || !got.ApplyToCodexModel ||
		*got.EnforcedServiceTier != "flex" || got.RequestPool.RequestsPerMinute != 3 || got.Fork.ParentPool.RequestsPerMinute != 9 || got.Fork.ParentName != "Parent" {
		t.Fatalf("inherited %+v %+v", got, got.Fork)
	}
	if fork.Fork.ParentPool != nil {
		t.Fatal("inheritance changed the stored fork")
	}
	parent.IsActive = false
	if InheritFromParent(fork, parent).IsActive {
		t.Fatal("a fork of a disabled parent is active")
	}
}

func TestForkSecretsHashInTheirOwnDomain(t *testing.T) {
	secret := ForkSecretPrefix + "abc"
	if APIKeyHash(secret) == ForkKeyHash(secret) || len(ForkKeyHash(secret)) != 64 {
		t.Fatal("a fork secret hashes like a plain key")
	}
}
