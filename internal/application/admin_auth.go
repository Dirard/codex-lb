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

type AuthState struct {
	Authenticated            bool     `json:"authenticated"`
	PasswordRequired         bool     `json:"passwordRequired"`
	TOTPRequiredOnLogin      bool     `json:"totpRequiredOnLogin"`
	TOTPConfigured           bool     `json:"totpConfigured"`
	BootstrapRequired        bool     `json:"bootstrapRequired"`
	BootstrapTokenRequired   bool     `json:"bootstrapTokenRequired"`
	BootstrapTokenConfigured bool     `json:"bootstrapTokenConfigured"`
	AuthMode                 string   `json:"authMode"`
	PasswordManagement       bool     `json:"passwordManagementEnabled"`
	PasswordSessionActive    bool     `json:"passwordSessionActive"`
	Role                     string   `json:"role"`
	Permissions              []string `json:"permissions"`
}

type adminSession struct {
	Kind        string `json:"kind"`
	Nonce       string `json:"nonce"`
	Expires     int64  `json:"expires"`
	PasswordTag string `json:"password_tag"`
	TOTPTag     string `json:"totp_tag"`
	TOTP        bool   `json:"totp"`
}

type authAttempts struct {
	Start time.Time
	Count int
}

type AdminAuth struct {
	settings Settings
	cipher   SecretCipher
	// Admin mutations are infrequent. Serialize changes and TOTP verification;
	// no upstream I/O occurs under this lock.
	mu       sync.Mutex
	attempts map[string]authAttempts
	now      func() time.Time
}

func NewAdminAuth(settings Settings, cipher SecretCipher) *AdminAuth {
	return &AdminAuth{settings: settings, cipher: cipher, attempts: make(map[string]authAttempts), now: time.Now}
}

func (a *AdminAuth) State(ctx context.Context, token string) (AuthState, error) {
	secret, err := a.settings.LoadAdminSecret(ctx)
	if err != nil {
		return AuthState{}, err
	}
	settings, err := a.settings.LoadSettings(ctx)
	if err != nil {
		return AuthState{}, err
	}
	session, valid := a.session(token, secret)
	configured := len(secret.TOTPSecretEncrypted) != 0
	full := valid && (!settings.TOTPRequiredOnLogin || configured && session.TOTP)
	return AuthState{
		Authenticated: full, PasswordRequired: secret.PasswordHash != "",
		TOTPRequiredOnLogin: valid && settings.TOTPRequiredOnLogin && !session.TOTP,
		TOTPConfigured:      configured, BootstrapRequired: secret.PasswordHash == "",
		AuthMode: "standard", PasswordManagement: true, PasswordSessionActive: full,
		Role: "admin", Permissions: []string{"read", "write"},
	}, nil
}

func (a *AdminAuth) Authorize(ctx context.Context, token string) error {
	state, err := a.State(ctx, token)
	if err != nil {
		return err
	}
	if !state.Authenticated {
		return ErrAuthentication
	}
	return nil
}

// SetupPassword is called only after the HTTP adapter verifies local bootstrap
// provenance or the installation's bootstrap token.
func (a *AdminAuth) SetupPassword(ctx context.Context, password string, ttl time.Duration) (string, error) {
	if err := validatePassword(password); err != nil {
		return "", err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return "", errors.New("could not hash dashboard password")
	}
	secret := domain.AdminSecret{PasswordHash: string(hash)}
	created, err := a.settings.InitializeAdminSecret(ctx, secret)
	if err != nil {
		return "", err
	}
	if !created {
		return "", &AuthError{Code: "password_already_configured", Message: "Password is already configured"}
	}
	return a.issue(secret, false, ttl)
}

func (a *AdminAuth) Login(ctx context.Context, client, password string, ttl time.Duration) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	secret, err := a.settings.LoadAdminSecret(ctx)
	if err != nil {
		return "", err
	}
	if secret.PasswordHash == "" {
		return "", &AuthError{Code: "password_not_configured", Message: "Password is not configured"}
	}
	if err := a.attempt("password:" + client); err != nil {
		return "", err
	}
	if len(password) > 72 || bcrypt.CompareHashAndPassword([]byte(secret.PasswordHash), []byte(password)) != nil {
		return "", ErrCredentials
	}
	delete(a.attempts, "password:"+client)
	settings, err := a.settings.LoadSettings(ctx)
	if err != nil {
		return "", err
	}
	if settings.TOTPRequiredOnLogin && ttl > 5*time.Minute {
		ttl = 5 * time.Minute
	}
	return a.issue(secret, false, ttl)
}

func (a *AdminAuth) ChangePassword(ctx context.Context, token, current, replacement string, ttl time.Duration) (string, error) {
	if err := validatePassword(replacement); err != nil {
		return "", err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	secret, err := a.authorizedSecret(ctx, token, true)
	if err != nil {
		return "", err
	}
	if bcrypt.CompareHashAndPassword([]byte(secret.PasswordHash), []byte(current)) != nil {
		return "", ErrCredentials
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(replacement), 12)
	if err != nil {
		return "", errors.New("could not hash dashboard password")
	}
	secret.PasswordHash = string(hash)
	if err := a.settings.SaveAdminSecret(ctx, secret); err != nil {
		return "", err
	}
	return a.issue(secret, true, ttl)
}

func (a *AdminAuth) RemovePassword(ctx context.Context, token, password string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	secret, err := a.authorizedSecret(ctx, token, true)
	if err != nil {
		return err
	}
	if bcrypt.CompareHashAndPassword([]byte(secret.PasswordHash), []byte(password)) != nil {
		return ErrCredentials
	}
	return a.settings.SaveAdminSecret(ctx, domain.AdminSecret{})
}

func (a *AdminAuth) authorizedSecret(ctx context.Context, token string, full bool) (domain.AdminSecret, error) {
	secret, err := a.settings.LoadAdminSecret(ctx)
	if err != nil {
		return secret, err
	}
	session, ok := a.session(token, secret)
	settings, err := a.settings.LoadSettings(ctx)
	if err != nil {
		return secret, err
	}
	if !ok || (full && settings.TOTPRequiredOnLogin && (len(secret.TOTPSecretEncrypted) == 0 || !session.TOTP)) {
		return secret, ErrAuthentication
	}
	return secret, nil
}

func (a *AdminAuth) session(token string, secret domain.AdminSecret) (adminSession, bool) {
	var session adminSession
	if token == "" || len(token) > 4096 || secret.PasswordHash == "" {
		return session, false
	}
	raw, err := a.cipher.Decrypt([]byte(token))
	if err != nil || json.Unmarshal(raw, &session) != nil || session.Kind != "codex-lb-go-admin-v1" || session.Nonce == "" || session.Expires <= a.now().Unix() {
		return session, false
	}
	if subtle.ConstantTimeCompare([]byte(session.PasswordTag), []byte(secretTag([]byte(secret.PasswordHash)))) != 1 {
		return session, false
	}
	// Changing the second factor invalidates older second-factor grants but keeps
	// the password-only state available for another verification.
	if session.TOTPTag != secretTag(secret.TOTPSecretEncrypted) {
		session.TOTP = false
	}
	return session, true
}

func (a *AdminAuth) issue(secret domain.AdminSecret, totp bool, ttl time.Duration) (string, error) {
	if ttl <= 0 || ttl > 366*24*time.Hour {
		return "", errors.New("invalid dashboard session lifetime")
	}
	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	raw, err := json.Marshal(adminSession{
		Kind: "codex-lb-go-admin-v1", Nonce: base64.RawURLEncoding.EncodeToString(nonce[:]),
		Expires: a.now().Add(ttl).Unix(), PasswordTag: secretTag([]byte(secret.PasswordHash)),
		TOTPTag: secretTag(secret.TOTPSecretEncrypted), TOTP: totp,
	})
	if err != nil {
		return "", err
	}
	encrypted, err := a.cipher.Encrypt(raw)
	return string(encrypted), err
}

func secretTag(value []byte) string { return fmt.Sprintf("%x", sha256.Sum256(value)) }

func validatePassword(password string) error {
	if !utf8.ValidString(password) || utf8.RuneCountInString(password) < 8 {
		return &AuthError{Code: "invalid_password", Message: "Password must be at least 8 characters"}
	}
	if len(password) > 72 {
		return &AuthError{Code: "password_too_long", Message: "Password must be at most 72 UTF-8 bytes"}
	}
	return nil
}

func (a *AdminAuth) attempt(client string) error {
	now := a.now()
	for key, state := range a.attempts {
		if !now.Before(state.Start.Add(time.Minute)) {
			delete(a.attempts, key)
		}
	}
	state, exists := a.attempts[client]
	if (!exists && len(a.attempts) >= 4096) || state.Count >= 8 {
		retry := 60
		if exists {
			retry = max(1, int(state.Start.Add(time.Minute).Sub(now).Seconds())+1)
		}
		return &AuthError{Code: "login_rate_limited", Message: "Too many login attempts", RetryAfter: retry}
	}
	if !exists {
		state.Start = now
	}
	state.Count++
	a.attempts[client] = state
	return nil
}
