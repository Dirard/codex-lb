package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"time"
)

// KeyFork marks an API key as a fork: a child key its parent's holder
// manages through the key API. A fork never exceeds its parent.
type KeyFork struct {
	ParentID   string    `json:"parentId"`
	ParentName string    `json:"parentName"`
	CreatedBy  KeyActor  `json:"createdBy"`
	UpdatedBy  KeyActor  `json:"updatedBy"`
	UpdatedAt  time.Time `json:"updatedAt"`
	// ParentPool is the parent's request pool, which the parent and its forks
	// share. It is read with the fork and never stored on it.
	ParentPool *RequestPool `json:"-"`
}

// KeyActor names who wrote a fork.
type KeyActor string

const (
	KeyActorOwner KeyActor = "owner"
	KeyActorAdmin KeyActor = "admin"
)

const (
	// ForkSecretPrefix starts every fork secret.
	ForkSecretPrefix = "sk-clb-fork-"
	// MaxForksPerKey bounds the forks of one parent.
	MaxForksPerKey = 100
)

// APIKeyHash is the stored hash of a key secret.
func APIKeyHash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// ForkKeyHash is the stored hash of a fork secret. Its own domain keeps code
// that predates forks from authenticating one: such code would serve the fork
// without its parent's limits.
func ForkKeyHash(secret string) string {
	sum := sha256.Sum256([]byte("codex-lb fork key\x00" + secret))
	return hex.EncodeToString(sum[:])
}

// HasRequestPool reports whether the key's model requests take pool slots:
// its own pool or, for a fork, its parent's.
func (k APIKey) HasRequestPool() bool {
	return k.RequestPool != nil || k.Fork != nil && k.Fork.ParentPool != nil
}

// ForkExceedsParent refuses a fork setting wider than its parent's.
type ForkExceedsParent struct {
	// Field is the setting's name in the key API.
	Field string
}

func (e *ForkExceedsParent) Error() string {
	return fmt.Sprintf("fork %s exceeds its parent", e.Field)
}

func (e *ForkExceedsParent) Unwrap() error { return ErrInvalid }

// ForkViolation reports the first setting of fork, as stored, that is wider
// than parent's current settings, or "" when the fork fits. Budget limits are
// free: the parent's limits apply to the fork as well.
func ForkViolation(parent, fork APIKey) string {
	if field := ForkSettingsViolation(parent, fork); field != "" {
		return field
	}
	if parent.ExpiresAt != nil && fork.ExpiresAt != nil && fork.ExpiresAt.After(*parent.ExpiresAt) {
		return "expiresAt"
	}
	if parent.RequestPool != nil && fork.RequestPool != nil && (fork.RequestPool.RequestsPerMinute > parent.RequestPool.RequestsPerMinute ||
		fork.RequestPool.MaxWaitSeconds > parent.RequestPool.MaxWaitSeconds) {
		return "requestPool"
	}
	return ""
}

// ForkSettingsViolation checks the settings a parent's change can make a fork
// exceed: models, reasoning efforts and service tier. Expiry and state are
// taken from the parent whenever the fork is read.
func ForkSettingsViolation(parent, fork APIKey) string {
	if len(parent.AllowedModels) > 0 && (len(fork.AllowedModels) == 0 || !subset(fork.AllowedModels, parent.AllowedModels)) {
		return "allowedModels"
	}
	if parent.EnforcedModel != nil {
		if fork.EnforcedModel != nil && *fork.EnforcedModel != *parent.EnforcedModel {
			return "enforcedModel"
		}
	} else if fork.EnforcedModel != nil && (!allows(parent.AllowedModels, *fork.EnforcedModel) || !allows(fork.AllowedModels, *fork.EnforcedModel)) {
		return "enforcedModel"
	}
	switch {
	case parent.EnforcedReasoningEffort != nil:
		if fork.AllowedReasoningEfforts != nil {
			return "allowedReasoningEfforts"
		}
		if fork.EnforcedReasoningEffort != nil && *fork.EnforcedReasoningEffort != *parent.EnforcedReasoningEffort {
			return "enforcedReasoningEffort"
		}
	case parent.AllowedReasoningEfforts != nil:
		if fork.EnforcedReasoningEffort != nil {
			if !slices.Contains(parent.AllowedReasoningEfforts, *fork.EnforcedReasoningEffort) {
				return "enforcedReasoningEffort"
			}
		} else if len(fork.AllowedReasoningEfforts) == 0 || !subset(fork.AllowedReasoningEfforts, parent.AllowedReasoningEfforts) {
			return "allowedReasoningEfforts"
		}
	}
	if parent.EnforcedServiceTier != nil && fork.EnforcedServiceTier != nil && *fork.EnforcedServiceTier != *parent.EnforcedServiceTier {
		return "enforcedServiceTier"
	}
	return ""
}

// InheritFromParent is the fork as it serves: its own model, effort, tier,
// limits and pool cap, the parent's enforcement where the parent enforces,
// the parent's scope and inherited settings, the earlier expiry, and active
// only while both keys are.
func InheritFromParent(fork, parent APIKey) APIKey {
	fork.IsActive = fork.IsActive && parent.IsActive
	if parent.ExpiresAt != nil && (fork.ExpiresAt == nil || parent.ExpiresAt.Before(*fork.ExpiresAt)) {
		fork.ExpiresAt = parent.ExpiresAt
	}
	if parent.EnforcedModel != nil {
		fork.EnforcedModel, fork.ApplyToCodexModel = parent.EnforcedModel, parent.ApplyToCodexModel
	}
	if parent.EnforcedReasoningEffort != nil {
		fork.EnforcedReasoningEffort, fork.AllowedReasoningEfforts = parent.EnforcedReasoningEffort, nil
	}
	if parent.EnforcedServiceTier != nil {
		fork.EnforcedServiceTier = parent.EnforcedServiceTier
	}
	fork.GroupID = parent.GroupID
	fork.AccountAssignmentScopeEnabled, fork.SourceAssignmentScopeEnabled = parent.AccountAssignmentScopeEnabled, parent.SourceAssignmentScopeEnabled
	fork.AssignedAccountIDs, fork.AssignedSourceIDs = parent.AssignedAccountIDs, parent.AssignedSourceIDs
	fork.TrafficClass, fork.TransportPolicyOverride, fork.UsageSections = parent.TrafficClass, parent.TransportPolicyOverride, parent.UsageSections
	if fork.Fork != nil {
		info := *fork.Fork
		info.ParentName, info.ParentPool = parent.Name, parent.RequestPool
		fork.Fork = &info
	}
	return fork
}

func subset(values, of []string) bool {
	for _, value := range values {
		if !slices.Contains(of, value) {
			return false
		}
	}
	return true
}

// allows reports whether a restriction list admits value; nil or empty
// admits everything.
func allows(list []string, value string) bool {
	return len(list) == 0 || slices.Contains(list, value)
}
