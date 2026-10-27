package sqlite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"codex-lb/internal/domain"
	"golang.org/x/crypto/bcrypt"
)

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func TestDashboardSessionRevocationEpochAndBootstrapConsumption(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "sessions.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	hash, _ := bcrypt.GenerateFromPassword([]byte("synthetic-password"), bcrypt.MinCost)
	if created, err := store.InitializeAdminSecret(ctx, domain.AdminSecret{PasswordHash: string(hash)}); err != nil || !created {
		t.Fatal("initialize admin secret", err)
	}
	if err := store.AdvanceDashboardSessionEpoch(ctx); err != nil {
		t.Fatal(err)
	}
	secret, err := store.LoadAdminSecret(ctx)
	if err != nil || secret.SessionEpoch != 1 {
		t.Fatalf("epoch = %d, %v", secret.SessionEpoch, err)
	}
	// Ordinary secret updates (password change, TOTP) keep the epoch.
	secret.PasswordHash = string(hash)
	if err := store.SaveAdminSecret(ctx, secret); err != nil {
		t.Fatal(err)
	}
	if secret, _ = store.LoadAdminSecret(ctx); secret.SessionEpoch != 1 {
		t.Fatal("saving the admin secret reset the session epoch")
	}
	expired, active := digest("expired-session"), digest("active-session")
	if err := store.RevokeDashboardSession(ctx, expired, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.RevokeDashboardSession(ctx, active, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if revoked, err := store.DashboardSessionRevoked(ctx, active); err != nil || !revoked {
		t.Fatal("revoked session not reported", err)
	}
	if revoked, _ := store.DashboardSessionRevoked(ctx, expired); revoked {
		t.Fatal("expired revocation was not pruned")
	}
	if revoked, _ := store.DashboardSessionRevoked(ctx, digest("other")); revoked {
		t.Fatal("unrelated session reported revoked")
	}
	if err := store.RevokeDashboardSession(ctx, "not-a-digest", time.Now()); !errors.Is(err, ErrInvalid) {
		t.Fatal("malformed digest accepted")
	}
	token := digest("operator-token")
	if consumed, _ := store.BootstrapTokenConsumed(ctx, token); consumed {
		t.Fatal("fresh token reported consumed")
	}
	for range 2 {
		if err := store.ConsumeBootstrapToken(ctx, token, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if consumed, _ := store.BootstrapTokenConsumed(ctx, token); !consumed {
		t.Fatal("consumed token not recorded")
	}
	// Removing the password keeps consumption evidence.
	if err := store.SaveAdminSecret(ctx, domain.AdminSecret{}); err != nil {
		t.Fatal(err)
	}
	if consumed, _ := store.BootstrapTokenConsumed(ctx, token); !consumed {
		t.Fatal("password removal forgot a consumed bootstrap token")
	}
}
