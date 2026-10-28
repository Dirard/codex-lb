package application

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
	"unicode/utf8"

	"codex-lb/internal/domain"
	"golang.org/x/crypto/bcrypt"
)

type SecretCipher interface {
	Encrypt([]byte) ([]byte, error)
	Decrypt([]byte) ([]byte, error)
}

type AuthError struct {
	Code       string
	Message    string
	RetryAfter int
}

func (e *AuthError) Error() string { return e.Message }

var ErrAuthentication = &AuthError{Code: "authentication_required", Message: "Authentication is required"}
var ErrCredentials = &AuthError{Code: "invalid_credentials", Message: "Invalid credentials"}

// ErrReauthentication rejects a sensitive action whose password re-entry is
// missing or wrong; it never ends the caller's session.
var ErrReauthentication = &AuthError{Code: "reauthentication_required", Message: "Confirm the dashboard password to continue"}

var errAuthBusy = &AuthError{Code: "authentication_busy", Message: "Authentication is busy; retry shortly", RetryAfter: 1}

const (
	adminSessionKind = "codex-lb-go-admin-v2"
	// DashboardIdleTimeout rejects a session that made no authenticated request.
	DashboardIdleTimeout = 12 * time.Hour
	// MaxDashboardSessionTTL caps every absolute session lifetime.
	MaxDashboardSessionTTL = 30 * 24 * time.Hour
	// PendingTOTPSessionTTL bounds a password-only session awaiting its code.
	PendingTOTPSessionTTL = 5 * time.Minute
	sessionRenewInterval  = 10 * time.Minute
	passwordHashSlots     = 4
)

type AuthState struct {
	Authenticated            bool     `json:"authenticated"`
	PasswordRequired         bool     `json:"passwordRequired"`
	TOTPRequiredOnLogin      bool     `json:"totpRequiredOnLogin"`
	TOTPConfigured           bool     `json:"totpConfigured"`
	BootstrapRequired        bool     `json:"bootstrapRequired"`
	BootstrapTokenRequired   bool     `json:"bootstrapTokenRequired"`
	BootstrapTokenConfigured bool     `json:"bootstrapTokenConfigured"`
	BootstrapTokenSource     string   `json:"bootstrapTokenSource,omitempty"`
	AuthMode                 string   `json:"authMode"`
	PasswordManagement       bool     `json:"passwordManagementEnabled"`
	PasswordSessionActive    bool     `json:"passwordSessionActive"`
	Role                     string   `json:"role"`
	Permissions              []string `json:"permissions"`
}

// SessionRenewal is a re-encrypted session with a later idle deadline.
type SessionRenewal struct {
	Token  string
	MaxAge time.Duration
}

type adminSession struct {
	Kind        string `json:"kind"`
	ID          string `json:"sid"`
	Epoch       int64  `json:"epoch"`
	Expires     int64  `json:"expires"`
	Idle        int64  `json:"idle"`
	PasswordTag string `json:"password_tag"`
	TOTPTag     string `json:"totp_tag"`
	TOTP        bool   `json:"totp"`
}

// AdminAuthStore is the persistence boundary of dashboard authentication.
type AdminAuthStore interface {
	Settings
	DashboardSessions
}

type AdminAuth struct {
	settings Settings
	sessions DashboardSessions
	cipher   SecretCipher
	// mu serializes admin-secret read-modify-write sequences. Password hashing
	// and the failure limiter never run under it.
	mu        sync.Mutex
	limiter   *authLimiter
	hashSlots chan struct{}
	bootstrap bootstrapGate
	now       func() time.Time
}

func NewAdminAuth(store AdminAuthStore, cipher SecretCipher) *AdminAuth {
	return &AdminAuth{
		settings: store, sessions: store, cipher: cipher, limiter: newAuthLimiter(),
		hashSlots: make(chan struct{}, passwordHashSlots), now: time.Now,
	}
}

func (a *AdminAuth) State(ctx context.Context, token string) (AuthState, error) {
	secret, err := a.settings.LoadAdminSecret(ctx)
	if err != nil {
		return AuthState{}, err
	}
	session, valid, err := a.session(ctx, token, secret)
	if err != nil {
		return AuthState{}, err
	}
	configured := len(secret.TOTPSecretEncrypted) != 0
	full := valid && (!configured || session.TOTP)
	return AuthState{
		Authenticated: full, PasswordRequired: secret.PasswordHash != "",
		TOTPRequiredOnLogin: valid && configured && !session.TOTP,
		TOTPConfigured:      configured, BootstrapRequired: secret.PasswordHash == "",
		AuthMode: "standard", PasswordManagement: true, PasswordSessionActive: full,
		Role: "admin", Permissions: []string{"read", "write"},
	}, nil
}

// Authorize accepts only a fully authenticated session and returns a renewal
// when the idle deadline should move forward.
func (a *AdminAuth) Authorize(ctx context.Context, token string) (SessionRenewal, error) {
	_, session, err := a.authorizedSecret(ctx, token, true)
	if err != nil {
		return SessionRenewal{}, err
	}
	now := a.now()
	idle := time.Unix(session.Idle, 0)
	expires := time.Unix(session.Expires, 0)
	if idle.Sub(now) > DashboardIdleTimeout-sessionRenewInterval || !idle.Before(expires) {
		return SessionRenewal{}, nil
	}
	session.Idle = min(now.Add(DashboardIdleTimeout).Unix(), session.Expires)
	renewed, err := a.seal(session)
	if err != nil {
		return SessionRenewal{}, err
	}
	return SessionRenewal{Token: renewed, MaxAge: expires.Sub(now)}, nil
}

// SetupPassword creates the first dashboard password. requireToken is false
// only when another configured authentication mode already admitted the caller.
func (a *AdminAuth) SetupPassword(ctx context.Context, client, bootstrapToken, password string, requireToken bool, ttl time.Duration) (string, error) {
	operator := false
	if requireToken {
		if err := a.limiter.begin(authScopeBootstrap, client, a.now()); err != nil {
			return "", err
		}
		var ok bool
		operator, ok = a.bootstrap.match(bootstrapToken)
		if !ok {
			a.limiter.fail(authScopeBootstrap, a.now())
			return "", errBootstrapToken
		}
		a.limiter.succeed(authScopeBootstrap, client)
	}
	if err := validatePassword(password); err != nil {
		return "", err
	}
	var hash []byte
	if err := a.hashing(func() (err error) {
		hash, err = bcrypt.GenerateFromPassword([]byte(password), 12)
		return err
	}); err != nil {
		return "", hashingError(err)
	}
	secret := domain.AdminSecret{PasswordHash: string(hash)}
	created, err := a.settings.InitializeAdminSecret(ctx, secret)
	if err != nil {
		return "", err
	}
	if !created {
		return "", &AuthError{Code: "password_already_configured", Message: "Password is already configured"}
	}
	if requireToken {
		if err := a.consumeBootstrap(ctx, operator); err != nil {
			return "", err
		}
	}
	return a.issue(secret, false, ttl)
}

func (a *AdminAuth) Login(ctx context.Context, client, password string, ttl time.Duration) (string, error) {
	secret, err := a.settings.LoadAdminSecret(ctx)
	if err != nil {
		return "", err
	}
	if secret.PasswordHash == "" {
		return "", &AuthError{Code: "password_not_configured", Message: "Password is not configured"}
	}
	if err := a.checkPassword(client, secret.PasswordHash, password, ErrCredentials); err != nil {
		return "", err
	}
	if len(secret.TOTPSecretEncrypted) != 0 && ttl > PendingTOTPSessionTTL {
		ttl = PendingTOTPSessionTTL
	}
	return a.issue(secret, false, ttl)
}

// Reauthenticate confirms the current password of a fully authenticated
// session before a sensitive action.
func (a *AdminAuth) Reauthenticate(ctx context.Context, token, client, password string) error {
	secret, _, err := a.authorizedSecret(ctx, token, true)
	if err != nil {
		return err
	}
	return a.checkPassword(client, secret.PasswordHash, password, ErrReauthentication)
}

func (a *AdminAuth) ChangePassword(ctx context.Context, token, client, current, replacement string, ttl time.Duration) (string, error) {
	if err := validatePassword(replacement); err != nil {
		return "", err
	}
	secret, _, err := a.authorizedSecret(ctx, token, true)
	if err != nil {
		return "", err
	}
	if err := a.checkPassword(client, secret.PasswordHash, current, ErrCredentials); err != nil {
		return "", err
	}
	var hash []byte
	if err := a.hashing(func() (err error) {
		hash, err = bcrypt.GenerateFromPassword([]byte(replacement), 12)
		return err
	}); err != nil {
		return "", hashingError(err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	latest, err := a.settings.LoadAdminSecret(ctx)
	if err != nil {
		return "", err
	}
	if latest.PasswordHash != secret.PasswordHash {
		return "", ErrAuthentication
	}
	latest.PasswordHash = string(hash)
	if err := a.settings.SaveAdminSecret(ctx, latest); err != nil {
		return "", err
	}
	return a.issue(latest, true, ttl)
}

func (a *AdminAuth) RemovePassword(ctx context.Context, token, client, password string) error {
	secret, _, err := a.authorizedSecret(ctx, token, true)
	if err != nil {
		return err
	}
	if err := a.checkPassword(client, secret.PasswordHash, password, ErrCredentials); err != nil {
		return err
	}
	a.mu.Lock()
	latest, err := a.settings.LoadAdminSecret(ctx)
	if err == nil && latest.PasswordHash != secret.PasswordHash {
		err = ErrAuthentication
	}
	if err == nil {
		err = a.settings.SaveAdminSecret(ctx, domain.AdminSecret{})
	}
	a.mu.Unlock()
	if err != nil {
		return err
	}
	return a.rearmBootstrap()
}

// Logout revokes the presented session until its absolute expiry. Unknown or
// already invalid tokens are ignored so logout always clears the browser.
func (a *AdminAuth) Logout(ctx context.Context, token string) error {
	session, ok := a.open(token)
	if !ok || session.Expires <= a.now().Unix() {
		return nil
	}
	return a.sessions.RevokeDashboardSession(ctx, sessionDigest(session.ID), time.Unix(session.Expires, 0))
}

// LogoutAll advances the session epoch, rejecting every issued session.
func (a *AdminAuth) LogoutAll(ctx context.Context, token string) error {
	if _, _, err := a.authorizedSecret(ctx, token, true); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sessions.AdvanceDashboardSessionEpoch(ctx)
}

func (a *AdminAuth) checkPassword(client, hash, password string, rejection error) error {
	return a.hashing(func() error {
		if err := a.limiter.begin(authScopePassword, client, a.now()); err != nil {
			return err
		}
		if len(password) > 72 || bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
			a.limiter.fail(authScopePassword, a.now())
			return rejection
		}
		a.limiter.succeed(authScopePassword, client)
		return nil
	})
}

// hashing runs fn in one of a few bcrypt slots; when all are busy the caller
// is refused immediately instead of queueing unauthenticated work.
func (a *AdminAuth) hashing(fn func() error) error {
	select {
	case a.hashSlots <- struct{}{}:
		defer func() { <-a.hashSlots }()
		return fn()
	default:
		return errAuthBusy
	}
}

func hashingError(err error) error {
	var auth *AuthError
	if errors.As(err, &auth) {
		return err
	}
	return errors.New("could not hash dashboard password")
}

func (a *AdminAuth) authorizedSecret(ctx context.Context, token string, full bool) (domain.AdminSecret, adminSession, error) {
	secret, err := a.settings.LoadAdminSecret(ctx)
	if err != nil {
		return secret, adminSession{}, err
	}
	session, ok, err := a.session(ctx, token, secret)
	if err != nil {
		return secret, session, err
	}
	if !ok || full && len(secret.TOTPSecretEncrypted) != 0 && !session.TOTP {
		return secret, session, ErrAuthentication
	}
	return secret, session, nil
}

func (a *AdminAuth) open(token string) (adminSession, bool) {
	var session adminSession
	if token == "" || len(token) > 4096 {
		return session, false
	}
	raw, err := a.cipher.Decrypt([]byte(token))
	if err != nil || json.Unmarshal(raw, &session) != nil || session.Kind != adminSessionKind || session.ID == "" {
		return adminSession{}, false
	}
	return session, true
}

func (a *AdminAuth) session(ctx context.Context, token string, secret domain.AdminSecret) (adminSession, bool, error) {
	if secret.PasswordHash == "" {
		return adminSession{}, false, nil
	}
	session, ok := a.open(token)
	now := a.now().Unix()
	if !ok || session.Expires <= now || session.Idle <= now || session.Epoch != secret.SessionEpoch {
		return adminSession{}, false, nil
	}
	if subtle.ConstantTimeCompare([]byte(session.PasswordTag), []byte(secretTag([]byte(secret.PasswordHash)))) != 1 {
		return adminSession{}, false, nil
	}
	revoked, err := a.sessions.DashboardSessionRevoked(ctx, sessionDigest(session.ID))
	if err != nil || revoked {
		return adminSession{}, false, err
	}
	// Changing the second factor invalidates older second-factor grants but keeps
	// the password-only state available for another verification.
	if session.TOTPTag != secretTag(secret.TOTPSecretEncrypted) {
		session.TOTP = false
	}
	return session, true, nil
}

func (a *AdminAuth) issue(secret domain.AdminSecret, totp bool, ttl time.Duration) (string, error) {
	if ttl <= 0 || ttl > MaxDashboardSessionTTL {
		return "", errors.New("invalid dashboard session lifetime")
	}
	var id [24]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	now := a.now()
	return a.seal(adminSession{
		Kind: adminSessionKind, ID: base64.RawURLEncoding.EncodeToString(id[:]), Epoch: secret.SessionEpoch,
		Expires: now.Add(ttl).Unix(), Idle: now.Add(min(ttl, DashboardIdleTimeout)).Unix(),
		PasswordTag: secretTag([]byte(secret.PasswordHash)), TOTPTag: secretTag(secret.TOTPSecretEncrypted), TOTP: totp,
	})
}

func (a *AdminAuth) seal(session adminSession) (string, error) {
	raw, err := json.Marshal(session)
	if err != nil {
		return "", err
	}
	encrypted, err := a.cipher.Encrypt(raw)
	return string(encrypted), err
}

func secretTag(value []byte) string { return fmt.Sprintf("%x", sha256.Sum256(value)) }

func sessionDigest(id string) string { return secretTag([]byte(id)) }

func validatePassword(password string) error {
	if !utf8.ValidString(password) || utf8.RuneCountInString(password) < 8 {
		return &AuthError{Code: "invalid_password", Message: "Password must be at least 8 characters"}
	}
	if len(password) > 72 {
		return &AuthError{Code: "password_too_long", Message: "Password must be at most 72 UTF-8 bytes"}
	}
	return nil
}
