package application

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

var errTOTPCode = &AuthError{Code: "invalid_totp_code", Message: "Invalid or already used verification code"}

type TOTPSetup struct {
	Secret     string `json:"secret"`
	OTPAuthURI string `json:"otpauthUri"`
}

func (a *AdminAuth) StartTOTP(ctx context.Context, token string) (TOTPSetup, error) {
	secret, _, err := a.authorizedSecret(ctx, token, true)
	if err != nil {
		return TOTPSetup{}, err
	}
	if len(secret.TOTPSecretEncrypted) != 0 {
		return TOTPSetup{}, &AuthError{Code: "invalid_totp_setup", Message: "TOTP is already configured"}
	}
	var raw [20]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return TOTPSetup{}, err
	}
	value := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw[:])
	query := url.Values{"secret": {value}, "issuer": {"codex-lb"}, "algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"}}
	return TOTPSetup{Secret: value, OTPAuthURI: "otpauth://totp/codex-lb:dashboard?" + query.Encode()}, nil
}

// ConfirmTOTP stores a verified secret. From then on every sign-in requires a
// code because full authentication is derived from the configured secret.
func (a *AdminAuth) ConfirmTOTP(ctx context.Context, token, client, value, code string, ttl time.Duration) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	secret, _, err := a.authorizedSecret(ctx, token, true)
	if err != nil {
		return "", err
	}
	if len(secret.TOTPSecretEncrypted) != 0 {
		return "", &AuthError{Code: "invalid_totp_setup", Message: "TOTP is already configured"}
	}
	if err := a.limiter.begin(authScopeTOTP, client, a.now()); err != nil {
		return "", err
	}
	step, ok := verifyTOTP(value, code, a.now(), nil)
	if !ok {
		a.limiter.fail(authScopeTOTP, a.now())
		return "", errTOTPCode
	}
	secret.TOTPSecretEncrypted, err = a.cipher.Encrypt([]byte(value))
	if err != nil {
		return "", err
	}
	secret.TOTPLastVerifiedStep = &step
	if err := a.settings.SaveAdminSecret(ctx, secret); err != nil {
		return "", err
	}
	a.limiter.succeed(authScopeTOTP, client)
	return a.issue(secret, true, ttl)
}

func (a *AdminAuth) VerifyTOTP(ctx context.Context, token, client, code string, ttl time.Duration, disable bool) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	secret, session, err := a.authorizedSecret(ctx, token, disable)
	if err != nil {
		return "", err
	}
	if len(secret.TOTPSecretEncrypted) == 0 {
		return "", errTOTPCode
	}
	if disable && !session.TOTP {
		return "", ErrAuthentication
	}
	if err := a.limiter.begin(authScopeTOTP, client, a.now()); err != nil {
		return "", err
	}
	value, err := a.cipher.Decrypt(secret.TOTPSecretEncrypted)
	if err != nil {
		return "", err
	}
	step, ok := verifyTOTP(string(value), code, a.now(), secret.TOTPLastVerifiedStep)
	if !ok {
		a.limiter.fail(authScopeTOTP, a.now())
		return "", errTOTPCode
	}
	advanced, err := a.settings.AdvanceTOTPStep(ctx, step)
	if err != nil {
		return "", err
	}
	if !advanced {
		a.limiter.fail(authScopeTOTP, a.now())
		return "", errTOTPCode
	}
	if disable {
		secret.TOTPSecretEncrypted = nil
		secret.TOTPLastVerifiedStep = nil
		if err := a.settings.SaveAdminSecret(ctx, secret); err != nil {
			return "", err
		}
	}
	a.limiter.succeed(authScopeTOTP, client)
	return a.issue(secret, true, ttl)
}

func verifyTOTP(secret, code string, now time.Time, previous *int64) (int64, bool) {
	if len(secret) > 128 || len(code) != 6 {
		return 0, false
	}
	for _, char := range code {
		if char < '0' || char > '9' {
			return 0, false
		}
	}
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.TrimRight(secret, "=")))
	if err != nil || len(key) < 16 {
		return 0, false
	}
	for step := now.Unix()/30 - 1; step <= now.Unix()/30+1; step++ {
		if step < 0 || (previous != nil && step <= *previous) {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(totpCode(key, step)), []byte(code)) == 1 {
			return step, true
		}
	}
	return 0, false
}

func totpCode(key []byte, step int64) string {
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(step))
	mac := hmac.New(sha1.New, key) // RFC 6238 / legacy authenticator compatibility.
	mac.Write(counter[:])
	digest := mac.Sum(nil)
	offset := digest[len(digest)-1] & 15
	number := binary.BigEndian.Uint32(digest[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", number%1000000)
}
