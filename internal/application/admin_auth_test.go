package application

import (
	"context"
	"encoding/base32"
	"errors"
	"fmt"
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
	mu       sync.Mutex
	secret   domain.AdminSecret
	revoked  map[string]time.Time
	consumed map[string]bool
}

func (s *authSettings) LoadSettings(context.Context) (domain.RuntimeSettings, error) {
	return domain.RuntimeSettings{}, nil
}

func (s *authSettings) LoadAdminSecret(context.Context) (domain.AdminSecret, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.secret, nil
}

// SaveAdminSecret mirrors SQLite: the epoch is changed only by its own statement.
func (s *authSettings) SaveAdminSecret(_ context.Context, v domain.AdminSecret) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v.PasswordHash != "" {
		v.SessionEpoch = s.secret.SessionEpoch
	}
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

func (s *authSettings) RevokeDashboardSession(_ context.Context, digest string, expires time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revoked == nil {
		s.revoked = make(map[string]time.Time)
	}
	s.revoked[digest] = expires
	return nil
}

func (s *authSettings) DashboardSessionRevoked(_ context.Context, digest string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, revoked := s.revoked[digest]
	return revoked, nil
}

func (s *authSettings) AdvanceDashboardSessionEpoch(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secret.SessionEpoch++
	return nil
}

func (s *authSettings) BootstrapTokenConsumed(_ context.Context, digest string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.consumed[digest], nil
}

func (s *authSettings) ConsumeBootstrapToken(_ context.Context, digest string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.consumed == nil {
		s.consumed = make(map[string]bool)
	}
	s.consumed[digest] = true
	return nil
}

type memoryTokenFile struct{ token string }

func (f *memoryTokenFile) Publish(token string) error { f.token = token; return nil }
func (f *memoryTokenFile) Withdraw() error            { f.token = ""; return nil }
func (f *memoryTokenFile) Location() string           { return "/data/dashboard-bootstrap-token" }

const testOperatorToken = "operator-bootstrap-token-0123456789abcdef"

func newTestAdminAuth(t *testing.T) (*AdminAuth, *authSettings, *time.Time) {
	t.Helper()
	vault, err := credentials.Open(filepath.Join(t.TempDir(), "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	store := &authSettings{}
	auth := NewAdminAuth(store, vault)
	now := time.Unix(1_800_000_000, 0)
	auth.now = func() time.Time { return now }
	return auth, store, &now
}

func authorized(auth *AdminAuth, token string) bool {
	_, err := auth.Authorize(context.Background(), token)
	return err == nil
}

func TestAdminAuthLifecycle(t *testing.T) {
	ctx := context.Background()
	auth, _, now := newTestAdminAuth(t)
	auth.ConfigureBootstrap(testOperatorToken, nil, nil)
	state, err := auth.State(ctx, "")
	if err != nil || state.Authenticated || !state.BootstrapRequired {
		t.Fatalf("unconfigured auth state: %+v, %v", state, err)
	}
	if _, err := auth.SetupPassword(ctx, "local", testOperatorToken, strings.Repeat("🔒", 19), true, time.Hour); err == nil {
		t.Fatal("oversized UTF-8 password accepted")
	}
	if _, err := auth.Login(ctx, "local", "wrong", time.Hour); err == nil || len(auth.limiter.clients) != 0 {
		t.Fatal("unconfigured login spent rate limit")
	}
	token, err := auth.SetupPassword(ctx, "local", testOperatorToken, "test-only-password", true, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !authorized(auth, token) {
		t.Fatal("setup session not authorized")
	}
	legacy, _ := auth.cipher.Encrypt([]byte(`{"kind":"codex-lb-go-admin-v1","nonce":"n","expires":2000000000,"password_tag":"x"}`))
	if authorized(auth, string(legacy)) {
		t.Fatal("previous session format trusted")
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
	if !authorized(auth, full) || authorized(auth, token) {
		t.Fatal("TOTP enable did not invalidate old full grants")
	}
	// A configured second factor is required without any separate setting.
	pending, err := auth.Login(ctx, "local", "test-only-password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if authorized(auth, pending) {
		t.Fatal("password-only session bypassed configured TOTP")
	}
	if state, _ := auth.State(ctx, pending); state.Authenticated || !state.TOTPRequiredOnLogin {
		t.Fatalf("pending state = %+v", state)
	}
	if _, err := auth.VerifyTOTP(ctx, pending, "local", code, time.Hour, false); err == nil {
		t.Fatal("TOTP replay accepted")
	}
	*now = now.Add(30 * time.Second)
	full, err = auth.VerifyTOTP(ctx, pending, "local", totpCode(key, now.Unix()/30), time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := auth.ChangePassword(ctx, full, "local", "test-only-password", "new-test-password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if authorized(auth, full) || !authorized(auth, replacement) {
		t.Fatal("password change did not revoke old sessions")
	}
	if err := auth.Reauthenticate(ctx, replacement, "local", "test-only-password"); !errors.Is(err, ErrReauthentication) {
		t.Fatalf("stale password confirmed a sensitive action: %v", err)
	}
	if err := auth.Reauthenticate(ctx, replacement, "local", "new-test-password"); err != nil {
		t.Fatal(err)
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
	*now = now.Add(time.Minute)
	if _, err := auth.Login(ctx, "remote", "new-test-password", time.Hour); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Hour)
	if authorized(auth, replacement) {
		t.Fatal("expired session accepted")
	}
}

func TestBootstrapTokensAreOneTime(t *testing.T) {
	ctx := context.Background()
	auth, store, _ := newTestAdminAuth(t)
	file := &memoryTokenFile{}
	auth.ConfigureBootstrap(testOperatorToken, file, nil)
	if err := auth.PrepareBootstrap(ctx); err != nil || file.token != "" || auth.BootstrapSource() != BootstrapSourceEnvironment {
		t.Fatalf("operator token not armed: %v %q %q", err, file.token, auth.BootstrapSource())
	}
	if _, err := auth.SetupPassword(ctx, "client", "", "test-only-password", true, time.Hour); !errors.Is(err, errBootstrapToken) {
		t.Fatalf("setup without token: %v", err)
	}
	token, err := auth.SetupPassword(ctx, "client", testOperatorToken, "test-only-password", true, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if auth.BootstrapSource() != "" || len(store.consumed) != 1 {
		t.Fatal("operator token still armed after setup")
	}
	if err := auth.RemovePassword(ctx, token, "client", "test-only-password"); err != nil {
		t.Fatal(err)
	}
	if file.token == "" || auth.BootstrapSource() != BootstrapSourceFile {
		t.Fatal("password removal did not publish a fresh one-time token")
	}
	if _, err := auth.SetupPassword(ctx, "client", testOperatorToken, "test-only-password", true, time.Hour); !errors.Is(err, errBootstrapToken) {
		t.Fatalf("consumed operator token accepted again: %v", err)
	}
	if _, err := auth.SetupPassword(ctx, "client", file.token, "test-only-password", true, time.Hour); err != nil {
		t.Fatal(err)
	}
	if file.token != "" || auth.BootstrapSource() != "" {
		t.Fatal("generated token survived setup")
	}
	// A process restarted with the same operator token must not re-arm it.
	restarted := NewAdminAuth(&authSettings{consumed: store.consumed}, auth.cipher)
	restarted.ConfigureBootstrap(testOperatorToken, file, nil)
	if err := restarted.PrepareBootstrap(ctx); err != nil || restarted.BootstrapSource() != BootstrapSourceFile || file.token == "" {
		t.Fatalf("restart re-armed a consumed operator token: %v %q", err, restarted.BootstrapSource())
	}
}

func TestAuthFailuresAreLimitedPerInstallationAndPrefix(t *testing.T) {
	ctx := context.Background()
	auth, _, now := newTestAdminAuth(t)
	if _, err := auth.SetupPassword(ctx, "setup", "", "test-only-password", false, time.Hour); err != nil {
		t.Fatal(err)
	}
	for i := range authSharedFreeErrors {
		if _, err := auth.Login(ctx, fmt.Sprintf("198.51.100.%d", i+1), "incorrect", time.Hour); !errors.Is(err, ErrCredentials) {
			t.Fatalf("failure %d: %v", i, err)
		}
	}
	// Even the correct password from a new client waits for the shared delay.
	_, err := auth.Login(ctx, "203.0.113.200", "test-only-password", time.Hour)
	var rate *AuthError
	if !errors.As(err, &rate) || rate.Code != "login_rate_limited" || rate.RetryAfter != 1 {
		t.Fatalf("installation-wide limit not enforced for a new client: %v", err)
	}
	*now = now.Add(time.Second)
	if _, err := auth.Login(ctx, "198.51.100.99", "incorrect", time.Hour); !errors.Is(err, ErrCredentials) {
		t.Fatalf("attempt after the delay: %v", err)
	}
	if _, err := auth.Login(ctx, "203.0.113.200", "test-only-password", time.Hour); !errors.As(err, &rate) || rate.RetryAfter != 2 {
		t.Fatalf("delay did not grow exponentially: %v", err)
	}
	*now = now.Add(authSharedQuiet)
	if _, err := auth.Login(ctx, "203.0.113.200", "test-only-password", time.Hour); err != nil {
		t.Fatalf("limit did not decay after a quiet period: %v", err)
	}
	for i := range authClientAttempts {
		client := fmt.Sprintf("2001:db8:1:2::%x", i+1)
		if _, err := auth.Login(ctx, client, "incorrect", time.Hour); !errors.Is(err, ErrCredentials) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if _, err := auth.Login(ctx, "2001:db8:1:2:ffff::1", "test-only-password", time.Hour); !errors.As(err, &rate) {
		t.Fatalf("IPv6 /64 not grouped: %v", err)
	}
	if _, err := auth.Login(ctx, "2001:db8:1:3::1", "test-only-password", time.Hour); err != nil {
		t.Fatalf("another /64 was limited: %v", err)
	}
}

func TestPasswordHashingRefusesWhenSlotsAreBusy(t *testing.T) {
	ctx := context.Background()
	auth, _, _ := newTestAdminAuth(t)
	if _, err := auth.SetupPassword(ctx, "setup", "", "test-only-password", false, time.Hour); err != nil {
		t.Fatal(err)
	}
	for range cap(auth.hashSlots) {
		auth.hashSlots <- struct{}{}
	}
	_, err := auth.Login(ctx, "client", "test-only-password", time.Hour)
	var busy *AuthError
	if !errors.As(err, &busy) || busy.Code != "authentication_busy" || busy.RetryAfter != 1 {
		t.Fatalf("saturated hashing did not fail fast: %v", err)
	}
	if len(auth.limiter.clients) != 0 {
		t.Fatal("busy refusal spent the client's attempt budget")
	}
	if state, err := auth.State(ctx, ""); err != nil || !state.PasswordRequired {
		t.Fatal("session state blocked by hashing")
	}
}

func TestSessionsAreRevocableAndExpireWhenIdle(t *testing.T) {
	ctx := context.Background()
	auth, _, now := newTestAdminAuth(t)
	first, err := auth.SetupPassword(ctx, "setup", "", "test-only-password", false, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	second, err := auth.Login(ctx, "client", "test-only-password", 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.Logout(ctx, first); err != nil || authorized(auth, first) || !authorized(auth, second) {
		t.Fatal("logout did not revoke exactly the presented session")
	}
	third, err := auth.Login(ctx, "client", "test-only-password", 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.LogoutAll(ctx, third); err != nil || authorized(auth, second) || authorized(auth, third) {
		t.Fatal("logout everywhere left a session valid")
	}
	if err := auth.LogoutAll(ctx, third); !errors.Is(err, ErrAuthentication) {
		t.Fatal("logout everywhere accepted a revoked session")
	}
	active, err := auth.Login(ctx, "client", "test-only-password", 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Minute)
	if renewal, err := auth.Authorize(ctx, active); err != nil || renewal.Token != "" {
		t.Fatal("session renewed before the renewal interval")
	}
	*now = now.Add(sessionRenewInterval)
	renewal, err := auth.Authorize(ctx, active)
	if err != nil || renewal.Token == "" || renewal.MaxAge <= 0 || renewal.MaxAge > 24*time.Hour {
		t.Fatalf("active session not renewed: %+v %v", renewal, err)
	}
	*now = now.Add(DashboardIdleTimeout - time.Minute)
	if authorized(auth, active) || !authorized(auth, renewal.Token) {
		t.Fatal("idle deadline not enforced or renewal not honored")
	}
	*now = now.Add(DashboardIdleTimeout + time.Minute)
	if authorized(auth, renewal.Token) {
		t.Fatal("idle session accepted")
	}
	if err := auth.Logout(ctx, renewal.Token); err != nil {
		t.Fatal("logout of an expired session failed")
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
