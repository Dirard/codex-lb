package domain

import (
	"strings"
	"testing"
)

func TestCapabilityLineageHashIsDomainAndScopeSeparated(t *testing.T) {
	alias := CapabilityLineageAlias{Kind: "session_header", Value: "session"}
	first, err := CapabilityLineageMarkerHash(TrustedCyberCapability, "key-a", alias)
	if err != nil {
		t.Fatal(err)
	}
	otherKey, err := CapabilityLineageMarkerHash(TrustedCyberCapability, "key-b", alias)
	if err != nil {
		t.Fatal(err)
	}
	otherAlias, err := CapabilityLineageMarkerHash(TrustedCyberCapability, "key-a", CapabilityLineageAlias{Kind: "session_header", Value: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if first == otherKey || first == otherAlias || len(first) != 64 || strings.Contains(first, "session") || strings.Contains(first, "key-a") {
		t.Fatalf("lineage marker is not opaque and separated: %q %q %q", first, otherKey, otherAlias)
	}
}

func TestNormalizeCapabilityAliasesDeduplicatesAndRejectsAmbiguousValues(t *testing.T) {
	got := NormalizeCapabilityAliases([]CapabilityLineageAlias{
		{Kind: " session_header ", Value: " session "},
		{Kind: "session_header", Value: "session"},
		{Kind: "", Value: "empty"},
		{Kind: "turn_state", Value: "a\x00b"},
	})
	if len(got) != 1 || got[0].Kind != "session_header" || got[0].Value != "session" {
		t.Fatalf("aliases = %#v", got)
	}
}
