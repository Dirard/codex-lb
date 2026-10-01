package application

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"codex-lb/internal/domain"
)

// accountFromTokens derives account metadata from the id_token payload.
// JWT claims are metadata only; authorization never relies on them.
func (s *AccountsService) accountFromTokens(tokens authFileTokens, intendedAccountID string) (domain.Account, domain.AccountCredential, error) {
	claims := parseIDTokenClaims(tokens.IDToken)
	rawAccountID := firstNonEmptyString(derefString(tokens.AccountID), claims.auth.chatgptAccountID(), claims.chatgptAccountID)
	email := firstNonEmptyString(claims.email, defaultAccountEmail)
	workspaceID := cleanIdentityPart(firstNonEmptyString(claims.auth.workspaceID(), claims.workspaceID))
	workspaceLabel := cleanIdentityPart(firstNonEmptyString(claims.auth.workspaceLabel(), claims.workspaceLabel))
	now := s.now().UTC()
	account := domain.Account{
		ID:               intendedAccountID,
		Kind:             domain.AccountChatGPT,
		Provider:         "openai",
		ChatGPTAccountID: rawAccountID,
		ChatGPTUserID:    derefString(cleanIdentityPart(firstNonEmptyString(claims.auth.chatgptUserID(), claims.chatgptUserID, claims.sub))),
		Email:            email,
		WorkspaceID:      derefString(workspaceID),
		WorkspaceLabel:   derefString(workspaceLabel),
		SeatType:         normalizeSeatType(firstNonEmptyString(claims.auth.seatType(), claims.seatType)),
		PlanType:         coerceAccountPlanType(firstNonEmptyString(claims.auth.chatgptPlanType(), claims.chatgptPlanType)),
		RoutingPolicy:    "normal",
		Status:           domain.AccountActive,
		LastRefresh:      &now,
	}
	if account.ID == "" {
		account.ID = uniqueAccountID(rawAccountID, email, account.WorkspaceID, account.WorkspaceLabel)
	}
	credential, err := encryptCredential(s.cipher, account.ID, tokens)
	if err != nil {
		return account, domain.AccountCredential{}, err
	}
	return account, credential, nil
}

func encryptCredential(cipher SecretCipher, accountID string, tokens authFileTokens) (domain.AccountCredential, error) {
	access, err := cipher.Encrypt([]byte(tokens.AccessToken))
	if err != nil {
		return domain.AccountCredential{}, err
	}
	refresh, err := cipher.Encrypt([]byte(tokens.RefreshToken))
	if err != nil {
		return domain.AccountCredential{}, err
	}
	id, err := cipher.Encrypt([]byte(tokens.IDToken))
	if err != nil {
		return domain.AccountCredential{}, err
	}
	return domain.AccountCredential{
		AccountID:             accountID,
		AccessTokenEncrypted:  access,
		RefreshTokenEncrypted: refresh,
		IDTokenEncrypted:      id,
	}, nil
}

type idTokenClaims struct {
	email             string
	sub               string
	chatgptAccountID  string
	chatgptUserID     string
	chatgptPlanType   string
	workspaceID       string
	workspaceLabel    string
	seatType          string
	expiryEpochMillis int64
	auth              openAIAuthClaims
}

type openAIAuthClaims struct {
	values map[string]any
}

func (c openAIAuthClaims) first(keys ...string) string {
	for _, key := range keys {
		if value, ok := c.values[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (c openAIAuthClaims) chatgptAccountID() string { return c.first("chatgpt_account_id") }
func (c openAIAuthClaims) chatgptUserID() string {
	return c.first("chatgpt_user_id", "user_id", "chatgpt_account_user_id")
}
func (c openAIAuthClaims) chatgptPlanType() string { return c.first("chatgpt_plan_type") }
func (c openAIAuthClaims) workspaceID() string {
	return c.first("workspace_id", "chatgpt_workspace_id", "organization_id", "org_id", "tenant_id")
}
func (c openAIAuthClaims) workspaceLabel() string {
	return c.first("workspace_label", "workspace_name", "organization_name", "org_name", "tenant_name")
}
func (c openAIAuthClaims) seatType() string {
	return c.first("seat_type", "chatgpt_seat_type", "entitlement_type")
}

// parseIDTokenClaims decodes the unverified JWT payload. The id_token arrives
// over TLS directly from the OAuth token endpoint and is used only to label
// the account; it never authorizes anything by itself.
func parseIDTokenClaims(token string) idTokenClaims {
	var claims idTokenClaims
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return claims
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return claims
	}
	var raw map[string]any
	if json.Unmarshal(payload, &raw) != nil {
		return claims
	}
	read := func(keys ...string) string {
		for _, key := range keys {
			if value, ok := raw[key].(string); ok {
				return value
			}
		}
		return ""
	}
	claims = idTokenClaims{
		email:            read("email"),
		sub:              read("sub"),
		chatgptAccountID: read("chatgpt_account_id"),
		chatgptUserID:    read("chatgpt_user_id"),
		chatgptPlanType:  read("chatgpt_plan_type"),
		workspaceID:      read("workspace_id", "chatgpt_workspace_id", "organization_id", "org_id", "tenant_id"),
		workspaceLabel:   read("workspace_label", "workspace_name", "organization_name", "org_name", "tenant_name"),
		seatType:         read("seat_type", "chatgpt_seat_type", "entitlement_type"),
	}
	if auth, ok := raw["https://api.openai.com/auth"].(map[string]any); ok {
		claims.auth = openAIAuthClaims{values: auth}
	}
	switch exp := raw["exp"].(type) {
	case float64:
		claims.expiryEpochMillis = int64(exp * 1000)
	case string:
		var parsed int64
		if _, err := fmt.Sscanf(exp, "%d", &parsed); err == nil && parsed > 0 {
			claims.expiryEpochMillis = parsed * 1000
		}
	}
	return claims
}

func cleanIdentityPart(value string) *string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func normalizeSeatType(value string) string {
	cleaned := cleanIdentityPart(value)
	if cleaned == nil {
		return ""
	}
	return strings.ToLower(strings.ReplaceAll(*cleaned, "-", "_"))
}

func coerceAccountPlanType(value string) string {
	cleaned := strings.TrimSpace(value)
	if cleaned == "" {
		return defaultAccountPlan
	}
	lower := strings.ToLower(cleaned)
	if accountPlanTypes[lower] {
		return lower
	}
	return defaultAccountPlan
}

func uniqueAccountID(accountID, email, workspaceID, workspaceLabel string) string {
	workspaceKey := firstNonEmptyString(workspaceID, workspaceLabel)
	if accountID != "" && workspaceKey != "" {
		return accountID + "_" + hashPrefix(workspaceKey, 8)
	}
	if accountID != "" && email != "" && email != defaultAccountEmail {
		return accountID + "_" + hashPrefix(email, 8)
	}
	if accountID != "" {
		return accountID
	}
	if email != "" && email != defaultAccountEmail {
		return "email_" + hashPrefix(email, 12)
	}
	var entropy [6]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "local_" + hashPrefix(time.Now().Format(time.RFC3339Nano), 12)
	}
	return fmt.Sprintf("local_%x", entropy[:])
}

func hashPrefix(value string, length int) string {
	digest := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", digest[:])[:length]
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
