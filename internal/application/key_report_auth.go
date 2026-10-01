package application

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"time"

	"codex-lb/internal/domain"
)

type KeyReportKeys interface {
	GetAPIKey(context.Context, string) (domain.APIKey, error)
}

type KeyReportAuth struct {
	keys   KeyReportKeys
	cipher SecretCipher
	now    func() time.Time
}

type keyReportGrant struct {
	Kind    string `json:"kind"`
	KeyID   string `json:"key_id"`
	KeyTag  string `json:"key_tag"`
	Expires int64  `json:"expires"`
}

const keyReportSessionKind = "codex-lb-go-key-reports-v1"

func NewKeyReportAuth(keys KeyReportKeys, cipher SecretCipher) *KeyReportAuth {
	return &KeyReportAuth{keys: keys, cipher: cipher, now: time.Now}
}

// Issue encrypts a read-only grant for an already authenticated key, never its credential.
func (a *KeyReportAuth) Issue(key domain.APIKey, ttl time.Duration) (string, time.Time, error) {
	now := a.now()
	if !validReportKey(key, now) {
		return "", time.Time{}, ErrAuthentication
	}
	if ttl <= 0 || ttl > 366*24*time.Hour {
		return "", time.Time{}, errors.New("invalid report session lifetime")
	}
	expires := now.Add(ttl).Truncate(time.Second)
	if key.ExpiresAt != nil && key.ExpiresAt.Before(expires) {
		expires = key.ExpiresAt.Truncate(time.Second)
	}
	if !expires.After(now) {
		return "", time.Time{}, ErrAuthentication
	}
	raw, err := json.Marshal(keyReportGrant{
		Kind: keyReportSessionKind, KeyID: key.ID, KeyTag: secretTag([]byte(key.KeyHash)), Expires: expires.Unix(),
	})
	if err != nil {
		return "", time.Time{}, err
	}
	encrypted, err := a.cipher.Encrypt(raw)
	return string(encrypted), expires, err
}

// Authenticate revalidates the grant and current key on every report/session read.
func (a *KeyReportAuth) Authenticate(ctx context.Context, token string) (domain.APIKey, error) {
	if token == "" || len(token) > 4096 {
		return domain.APIKey{}, ErrAuthentication
	}
	raw, err := a.cipher.Decrypt([]byte(token))
	var grant keyReportGrant
	if err != nil || json.Unmarshal(raw, &grant) != nil || grant.Kind != keyReportSessionKind || grant.Expires <= a.now().Unix() || grant.KeyID == "" {
		return domain.APIKey{}, ErrAuthentication
	}
	key, err := a.keys.GetAPIKey(ctx, grant.KeyID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.APIKey{}, ErrAuthentication
	}
	if err != nil {
		return domain.APIKey{}, err
	}
	if !validReportKey(key, a.now()) || subtle.ConstantTimeCompare([]byte(grant.KeyTag), []byte(secretTag([]byte(key.KeyHash)))) != 1 {
		return domain.APIKey{}, ErrAuthentication
	}
	return key, nil
}

func validReportKey(key domain.APIKey, now time.Time) bool {
	return key.ID != "" && !domain.IsInternalKey(key.ID) && key.KeyHash != "" && key.IsActive && (key.ExpiresAt == nil || key.ExpiresAt.After(now))
}
