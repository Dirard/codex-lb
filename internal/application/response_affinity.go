package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"codex-lb/internal/domain"
)

const maxAffinityHintBytes = 4096
const maxAffinityFingerprintPartBytes = 2048

// requestAffinity is a preference, not a continuation owner. The observed
// version must be retained even when a prompt-cache binding has expired.
type requestAffinity struct {
	keyID     string
	kind      domain.AffinityKind
	key       string
	accountID string
	version   int64
}

func (a *requestAffinity) preferredAccountID() string {
	if a == nil {
		return ""
	}
	return a.accountID
}

// save is called only after the selected account has a reservation and before
// dispatch. Lost compare-and-set races leave the request on its chosen account.
func (a *requestAffinity) save(ctx context.Context, store AffinityRoutingStore, accountID, reservationID string) error {
	if a == nil {
		return nil
	}
	_, err := store.SaveAffinity(ctx, domain.AffinityBinding{
		Key: a.key, Kind: a.kind, APIKeyID: a.keyID,
		AccountID: accountID, UpdatedAt: time.Now(),
	}, a.version, reservationID)
	return err
}

func lookupRequestAffinity(ctx context.Context, store AffinityRoutingStore, keyID string, identity conversationIdentity, clientAffinity string, request responseRequest, settings domain.RuntimeSettings) (*requestAffinity, error) {
	kind, value, err := classifyRequestAffinity(identity, clientAffinity, request, settings)
	if err != nil || kind == "" {
		return nil, err
	}
	a := &requestAffinity{keyID: keyID, kind: kind, key: affinityKey(keyID, kind, value...)}
	binding, err := store.LookupAffinity(ctx, keyID, kind, a.key)
	if errors.Is(err, domain.ErrNotFound) {
		return a, nil
	}
	if err != nil {
		return nil, err
	}
	a.version = binding.Version
	if kind != domain.AffinityPromptCache || time.Since(binding.UpdatedAt) < time.Duration(settings.OpenAICacheAffinityMaxAgeSeconds)*time.Second {
		a.accountID = binding.AccountID
	}
	return a, nil
}

func classifyRequestAffinity(identity conversationIdentity, clientAffinity string, request responseRequest, settings domain.RuntimeSettings) (domain.AffinityKind, []string, error) {
	cacheKey, err := explicitPromptCacheKey(request.Object)
	if err != nil {
		return "", nil, err
	}
	if cacheKey != "" {
		if identity.ThreadID != "" {
			if len(identity.SessionID) > maxAffinityHintBytes || len(identity.ThreadID) > maxAffinityHintBytes {
				return "", nil, invalidAffinityHint()
			}
			// Shared prompt prefixes do not make independent threads one owner.
			// Keep the upstream cache key intact; scope only the LB's preference.
			return domain.AffinityPromptCache, []string{"explicit-thread", identity.SessionID, identity.ThreadID, cacheKey}, nil
		}
		return domain.AffinityPromptCache, []string{"explicit", cacheKey}, nil
	}
	if !settings.StickyThreadsEnabled {
		return "", nil, nil
	}
	for _, hint := range []string{identity.SessionID, identity.ThreadID, clientAffinity} {
		if len(hint) > maxAffinityHintBytes {
			return "", nil, invalidAffinityHint()
		}
	}
	if identity.ThreadID != "" {
		return domain.AffinityStickyThread, []string{"thread", identity.SessionID, identity.ThreadID}, nil
	}
	if identity.SessionID != "" {
		return domain.AffinityCodexSession, []string{"session", identity.SessionID}, nil
	}
	if clientAffinity != "" {
		return domain.AffinityStickyThread, []string{"client", clientAffinity}, nil
	}
	if err := request.loadInput(); err != nil {
		return "", nil, err
	}
	for _, item := range request.Input {
		var message struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(item, &message) == nil && message.Role == "user" && len(message.Content) != 0 {
			return domain.AffinityPromptCache, []string{
				"fingerprint", request.Model,
				boundedAffinityPart(request.Object["instructions"]),
				boundedAffinityPart(message.Content),
			}, nil
		}
	}
	return "", nil, nil
}

func explicitPromptCacheKey(object map[string]json.RawMessage) (string, error) {
	var selected string
	for _, name := range []string{"prompt_cache_key", "promptCacheKey"} {
		raw, present := object[name]
		if !present || string(raw) == "null" {
			continue
		}
		var value string
		if json.Unmarshal(raw, &value) != nil || len(value) > maxAffinityHintBytes {
			return "", invalidAffinityHint()
		}
		if strings.TrimSpace(value) == "" {
			continue
		}
		if selected != "" && selected != value {
			return "", invalidAffinityHint()
		}
		selected = value
	}
	return selected, nil
}

func invalidAffinityHint() error {
	return &ProxyError{Code: "invalid_request", Status: 400, Message: "Invalid cache or session affinity hint"}
}

func boundedAffinityPart(raw json.RawMessage) string {
	part := string(raw)
	if len(part) > maxAffinityFingerprintPartBytes {
		part = part[:maxAffinityFingerprintPartBytes]
	}
	return strconv.Itoa(len(raw)) + ":" + part
}

// A framed, key-scoped hash avoids retaining raw client hints or prompt text.
func affinityKey(keyID string, kind domain.AffinityKind, values ...string) string {
	parts := append([]string{keyID, string(kind)}, values...)
	framed, _ := json.Marshal(parts)
	digest := sha256.Sum256(framed)
	return hex.EncodeToString(digest[:])
}
