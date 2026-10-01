package application

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/domain"
)

type reportAuthKeys struct {
	key domain.APIKey
	err error
}

func (s *reportAuthKeys) GetAPIKey(_ context.Context, id string) (domain.APIKey, error) {
	if id != s.key.ID {
		return domain.APIKey{}, domain.ErrNotFound
	}
	return s.key, s.err
}

func TestKeyReportAuthGrantLifecycle(t *testing.T) {
	ctx := context.Background()
	vault, err := credentials.Open(filepath.Join(t.TempDir(), "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	key := domain.APIKey{ID: "key-a", KeyHash: "synthetic-hash-a", IsActive: true}
	store := &reportAuthKeys{key: key}
	auth := NewKeyReportAuth(store, vault)
	auth.now = func() time.Time { return now }
	token, expires, err := auth.Issue(key, time.Hour)
	if err != nil || !expires.Equal(now.Add(time.Hour)) {
		t.Fatal("session not issued with the configured lifetime", err)
	}
	if got, err := auth.Authenticate(ctx, token); err != nil || got.ID != key.ID {
		t.Fatal("session did not resolve its key", err)
	}
	raw, err := vault.Decrypt([]byte(token))
	if err != nil || strings.Contains(string(raw), key.KeyHash) {
		t.Fatal("grant retained the credential hash instead of a fingerprint")
	}
	for _, mutate := range []func(*domain.APIKey){
		func(k *domain.APIKey) { k.IsActive = false },
		func(k *domain.APIKey) { k.KeyHash = "rotated-hash" },
		func(k *domain.APIKey) { k.ExpiresAt = &now },
		func(k *domain.APIKey) { k.ID = domain.LocalProxyKeyID },
	} {
		store.key = key
		mutate(&store.key)
		if _, err := auth.Authenticate(ctx, token); !errors.Is(err, ErrAuthentication) {
			t.Fatal("invalidated key retained report access")
		}
	}
	store.key = key
	store.err = domain.ErrNotFound
	if _, err := auth.Authenticate(ctx, token); !errors.Is(err, ErrAuthentication) {
		t.Fatal("deleted key retained access")
	}
	store.err = errors.New("temporary storage error")
	if _, err := auth.Authenticate(ctx, token); !errors.Is(err, store.err) {
		t.Fatal("storage failure disguised as invalid session")
	}
	store.err = nil
	for _, invalid := range []string{"", "altered-" + token, strings.Repeat("x", 4097)} {
		if _, err := auth.Authenticate(ctx, invalid); !errors.Is(err, ErrAuthentication) {
			t.Fatal("invalid token accepted")
		}
	}
	var grant keyReportGrant
	if err := json.Unmarshal(raw, &grant); err != nil {
		t.Fatal(err)
	}
	grant.Kind = "codex-lb-go-admin-v1"
	raw, _ = json.Marshal(grant)
	wrongKind, _ := vault.Encrypt(raw)
	if _, err := auth.Authenticate(ctx, string(wrongKind)); !errors.Is(err, ErrAuthentication) {
		t.Fatal("wrong purpose accepted")
	}
	now = expires
	if _, err := auth.Authenticate(ctx, token); !errors.Is(err, ErrAuthentication) {
		t.Fatal("expired grant accepted")
	}
	keyExpiry := now.Add(5 * time.Minute)
	key.ExpiresAt = &keyExpiry
	if _, expires, err := auth.Issue(key, time.Hour); err != nil || !expires.Equal(keyExpiry) {
		t.Fatal("session outlived key expiry")
	}
	for _, ttl := range []time.Duration{0, -time.Hour, 367 * 24 * time.Hour} {
		if _, _, err := auth.Issue(key, ttl); err == nil {
			t.Fatal("invalid session lifetime accepted")
		}
	}
	key.ID = domain.LocalProxyKeyID
	if _, _, err := auth.Issue(key, time.Hour); !errors.Is(err, ErrAuthentication) {
		t.Fatal("internal key issued a session")
	}
}
