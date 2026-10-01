package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

type tokenView struct {
	ExpiresAt *time.Time `json:"expiresAt"`
	State     string     `json:"state"`
}

type accountAuthView struct {
	Access  tokenView `json:"access"`
	Refresh tokenView `json:"refresh"`
	IDToken tokenView `json:"idToken"`
}

// Claims are display metadata only: parsing an unverified JWT here never grants
// access, changes account ownership, or authorizes an upstream request.
func tokenClaims(value []byte) map[string]json.RawMessage {
	if len(value) > 128<<10 {
		return nil
	}
	parts := strings.Split(string(value), ".")
	if len(parts) != 3 {
		return nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var claims map[string]json.RawMessage
	if json.Unmarshal(decoded, &claims) != nil {
		return nil
	}
	return claims
}

func claimTime(raw json.RawMessage) *time.Time {
	var value string
	if json.Unmarshal(raw, &value) == nil {
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err == nil {
			parsed = parsed.UTC()
			return &parsed
		}
	}
	var seconds int64
	if json.Unmarshal(raw, &seconds) == nil && seconds > 0 && seconds <= 253402300799 {
		parsed := time.Unix(seconds, 0).UTC()
		return &parsed
	}
	return nil
}

func accountAuthentication(cipher application.SecretCipher, credential domain.AccountCredential) (accountAuthView, *time.Time) {
	var view accountAuthView
	var subscription *time.Time
	for _, entry := range []struct {
		encrypted []byte
		dst       *tokenView
		id        bool
	}{
		{credential.AccessTokenEncrypted, &view.Access, false},
		{credential.RefreshTokenEncrypted, &view.Refresh, false},
		{credential.IDTokenEncrypted, &view.IDToken, true},
	} {
		entry.dst.State = "missing"
		if len(entry.encrypted) == 0 {
			continue
		}
		plain, err := cipher.Decrypt(entry.encrypted)
		if err != nil {
			entry.dst.State = "unreadable"
			continue
		}
		entry.dst.State = "present"
		claims := tokenClaims(plain)
		entry.dst.ExpiresAt = claimTime(claims["exp"])
		if entry.dst.ExpiresAt != nil && !entry.dst.ExpiresAt.After(time.Now()) {
			entry.dst.State = "expired"
		}
		if entry.id {
			var auth map[string]json.RawMessage
			if json.Unmarshal(claims["https://api.openai.com/auth"], &auth) == nil {
				subscription = claimTime(auth["chatgpt_subscription_active_until"])
			}
		}
	}
	return view, subscription
}
