package application

import (
	"context"
	"encoding/base32"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/domain"
)

type authSettings struct {
	Settings
	mu          sync.Mutex
	secret      domain.AdminSecret
	requireTOTP bool
}

func (s *authSettings) LoadSettings(context.Context) (domain.RuntimeSettings, error) {
	return domain.RuntimeSettings{TOTPRequiredOnLogin: s.requireTOTP}, nil
}

func (s *authSettings) LoadAdminSecret(context.Context) (domain.AdminSecret, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.secret, nil
}

func (s *authSettings) SaveAdminSecret(_ context.Context, v domain.AdminSecret) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secret = v
	return nil
}

func (s *authSettings) InitializeAdminSecret(_ context.Context, v domain.AdminSecret) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.secret.PasswordHash != "" {
		return false, nil
	}
	s.secret = v
	return true, nil
}

func (s *authSettings) AdvanceTOTPStep(_ context.Context, step int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.secret.TOTPLastVerifiedStep != nil && *s.secret.TOTPLastVerifiedStep >= step {
		return false, nil
	}
	s.secret.TOTPLastVerifiedStep = &step
	return true, nil
}

func TestAdminAuthLifecycle(t *testing.T) {
	ctx := context.Background()
	vault, err := credentials.Open(filepath.Join(t.TempDir(), "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	auth := NewAdminAuth(&authSettings{}, vault)
	now := time.Unix(1_800_000_000, 0)
	auth.now = func() time.Time { return now }
	state, err := auth.State(ctx, "")
	if err != nil || state.Authenticated || !state.BootstrapRequired {
		t.Fatalf("unconfigured auth state: %+v, %v", state, err)
	}
	if _, err := auth.SetupPassword(ctx, strings.Repeat("🔒", 19), time.Hour); err == nil {
		t.Fatal("oversized UTF-8 password accepted")
	}
	if _, err := auth.Login(ctx, "local", "wrong", time.Hour); err == nil || len(auth.attempts) != 0 {
		t.Fatal("unconfigured login spent rate limit")
	}
	token, err := auth.SetupPassword(ctx, "test-only-password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.Authorize(ctx, token); err != nil {
		t.Fatal(err)
	}
	legacy, _ := vault.Encrypt([]byte(`{"exp":2000000000,"pw":true,"tv":true,"role":"admin"}`))
	if auth.Authorize(ctx, string(legacy)) == nil {
		t.Fatal("legacy cookie trusted")
	}
	setup, err := auth.StartTOTP(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(setup.Secret)
	code := totpCode(key, now.Unix()/30)
	full, err := auth.ConfirmTOTP(ctx, token, "local", setup.Secret, code, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	auth.settings.(*authSettings).requireTOTP = true
	if auth.Authorize(ctx, full) != nil || auth.Authorize(ctx, token) == nil {
		t.Fatal("TOTP enable did not invalidate old full grants")
	}
	pending, err := auth.Login(ctx, "local", "test-only-password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if auth.Authorize(ctx, pending) == nil {
		t.Fatal("password-only session bypassed TOTP")
	}
	if _, err := auth.VerifyTOTP(ctx, pending, "local", code, time.Hour, false); err == nil {
		t.Fatal("TOTP replay accepted")
	}
	now = now.Add(30 * time.Second)
	full, err = auth.VerifyTOTP(ctx, pending, "local", totpCode(key, now.Unix()/30), time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := auth.ChangePassword(ctx, full, "test-only-password", "new-test-password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if auth.Authorize(ctx, full) == nil || auth.Authorize(ctx, replacement) != nil {
		t.Fatal("password change did not revoke old sessions")
	}
	for range 8 {
		_, err := auth.Login(ctx, "remote", "incorrect", time.Hour)
		if !errors.Is(err, ErrCredentials) {
			t.Fatalf("unexpected failed-login error: %v", err)
		}
	}
	_, err = auth.Login(ctx, "remote", "new-test-password", time.Hour)
	var rate *AuthError
	if !errors.As(err, &rate) || rate.RetryAfter <= 0 {
		t.Fatal("login limiter not enforced")
	}
	now = now.Add(time.Minute)
	if _, err := auth.Login(ctx, "remote", "new-test-password", time.Hour); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	if auth.Authorize(ctx, replacement) == nil {
		t.Fatal("expired session accepted")
	}
}

func TestTOTPRFC6238AndPasswordBounds(t *testing.T) {
	// RFC 6238 SHA1 vector at t=59 is 94287082 (8 digits), 287082 with 6 digits.
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("12345678901234567890"))
	if step, valid := verifyTOTP(secret, "287082", time.Unix(59, 0), nil); !valid || step != 1 {
		t.Fatal("TOTP differs from RFC 6238")
	}
	if _, valid := verifyTOTP(secret, "287082", time.Unix(180, 0), nil); valid {
		t.Fatal("expired TOTP accepted")
	}
	if err := validatePassword(strings.Repeat("a", 72)); err != nil {
		t.Fatal(err)
	}
	if err := validatePassword(strings.Repeat("a", 73)); err == nil {
		t.Fatal("bcrypt truncation allowed")
	}
}
