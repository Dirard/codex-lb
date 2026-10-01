package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

const (
	RequiredCapabilityHeader = "X-Codex-LB-Required-Capability"
	TrustedCyberCapability   = "trusted_cyber"
	CapabilityMarkerDomain   = "capability-lineage/v1"
)

type CapabilityLineageAlias struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

func NormalizeCapabilityAliases(values []CapabilityLineageAlias) []CapabilityLineageAlias {
	unique := make(map[string]CapabilityLineageAlias, len(values))
	for _, alias := range values {
		alias.Kind = strings.TrimSpace(alias.Kind)
		alias.Value = strings.TrimSpace(alias.Value)
		if alias.Kind == "" || alias.Value == "" || strings.Contains(alias.Kind, "\x00") || strings.Contains(alias.Value, "\x00") {
			continue
		}
		unique[alias.Kind+"\x00"+alias.Value] = alias
	}
	result := make([]CapabilityLineageAlias, 0, len(unique))
	for _, alias := range unique {
		result = append(result, alias)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Kind != result[j].Kind {
			return result[i].Kind < result[j].Kind
		}
		return result[i].Value < result[j].Value
	})
	return result
}

func CapabilityLineageMarkerHash(capability, keyScope string, alias CapabilityLineageAlias) (string, error) {
	capability = strings.TrimSpace(capability)
	keyScope = strings.TrimSpace(keyScope)
	aliases := NormalizeCapabilityAliases([]CapabilityLineageAlias{alias})
	if capability == "" || keyScope == "" || len(aliases) != 1 {
		return "", ErrInvalid
	}
	if strings.Contains(capability, "\x00") || strings.Contains(keyScope, "\x00") {
		return "", ErrInvalid
	}
	alias = aliases[0]
	sum := sha256.Sum256([]byte(CapabilityMarkerDomain + "\x00" + capability + "\x00" + keyScope + "\x00" + alias.Kind + "\x00" + alias.Value))
	return hex.EncodeToString(sum[:]), nil
}

func CapabilityLineageMarkerHashes(capability, keyScope string, aliases []CapabilityLineageAlias) ([]string, error) {
	normalized := NormalizeCapabilityAliases(aliases)
	result := make([]string, 0, len(normalized))
	for _, alias := range normalized {
		hash, err := CapabilityLineageMarkerHash(capability, keyScope, alias)
		if err != nil {
			return nil, err
		}
		result = append(result, hash)
	}
	sort.Strings(result)
	return result, nil
}

func (a CapabilityLineageAlias) String() string {
	return fmt.Sprintf("%s:%s", a.Kind, a.Value)
}
