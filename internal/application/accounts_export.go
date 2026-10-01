package application

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"codex-lb/internal/domain"
)

type codexAuthTokensJSON struct {
	IDToken      string  `json:"id_token"`
	AccessToken  string  `json:"access_token"`
	RefreshToken string  `json:"refresh_token"`
	AccountID    *string `json:"account_id"`
}

type codexAuthJSON struct {
	AuthMode     string              `json:"auth_mode"`
	OpenAIAPIKey *string             `json:"OPENAI_API_KEY"`
	Tokens       codexAuthTokensJSON `json:"tokens"`
	LastRefresh  string              `json:"last_refresh"`
}

type openCodeOAuthJSON struct {
	Type      string  `json:"type"`
	Refresh   string  `json:"refresh"`
	Access    string  `json:"access"`
	Expires   int64   `json:"expires"`
	AccountID *string `json:"accountId"`
}

type openCodeAuthJSON struct {
	OpenAI openCodeOAuthJSON `json:"openai"`
}

type authExportAccount struct {
	AccountID        string  `json:"accountId"`
	ChatGPTAccountID *string `json:"chatgptAccountId"`
	Email            string  `json:"email"`
}

type authExportTokens struct {
	IDToken      string `json:"idToken"`
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresAtMs  int64  `json:"expiresAtMs"`
}

// AccountAuthExport is the authorized admin token export. Handlers must only
// return it over POST with no-store caching.
type AccountAuthExport struct {
	Filename         string            `json:"filename"`
	Account          authExportAccount `json:"account"`
	Tokens           authExportTokens  `json:"tokens"`
	CodexAuthJSON    codexAuthJSON     `json:"codexAuthJson"`
	OpenCodeAuthJSON openCodeAuthJSON  `json:"opencodeAuthJson"`
}

func (s *AccountsService) ExportAuth(ctx context.Context, id string) (AccountAuthExport, error) {
	account, err := s.visible(ctx, id)
	if err != nil {
		return AccountAuthExport{}, err
	}
	credential, err := s.accounts.GetAccountCredential(ctx, id)
	if err != nil {
		return AccountAuthExport{}, err
	}
	access, err := s.cipher.Decrypt(credential.AccessTokenEncrypted)
	if err != nil {
		return AccountAuthExport{}, err
	}
	refresh, err := s.cipher.Decrypt(credential.RefreshTokenEncrypted)
	if err != nil {
		return AccountAuthExport{}, err
	}
	idToken, err := s.cipher.Decrypt(credential.IDTokenEncrypted)
	if err != nil {
		return AccountAuthExport{}, err
	}
	expires := parseIDTokenClaims(string(access)).expiryEpochMillis
	lastRefresh := s.now().UTC()
	if account.LastRefresh != nil {
		lastRefresh = account.LastRefresh.UTC()
	}
	chatgptAccountID := nullable(account.ChatGPTAccountID)
	return AccountAuthExport{
		Filename: authExportFilename(account),
		Account: authExportAccount{
			AccountID: account.ID, ChatGPTAccountID: chatgptAccountID, Email: account.Email,
		},
		Tokens: authExportTokens{
			IDToken: string(idToken), AccessToken: string(access), RefreshToken: string(refresh),
			ExpiresAtMs: expires,
		},
		CodexAuthJSON: codexAuthJSON{
			AuthMode: "chatgpt",
			Tokens: codexAuthTokensJSON{
				IDToken: string(idToken), AccessToken: string(access), RefreshToken: string(refresh),
				AccountID: chatgptAccountID,
			},
			LastRefresh: lastRefresh.Format("2006-01-02T15:04:05.000000") + "Z",
		},
		OpenCodeAuthJSON: openCodeAuthJSON{
			OpenAI: openCodeOAuthJSON{
				Type: "oauth", Refresh: string(refresh), Access: string(access),
				Expires: expires, AccountID: chatgptAccountID,
			},
		},
	}, nil
}

var unsafeFilenameCharacters = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func authExportFilename(account domain.Account) string {
	source := account.Email
	if source == "" {
		source = account.ID
	}
	safe := strings.Trim(unsafeFilenameCharacters.ReplaceAllString(source, "-"), "-._")
	if safe == "" {
		safe = account.ID
	}
	return fmt.Sprintf("opencode-auth-%s.json", safe)
}

func exportTimeFormat(value time.Time) string {
	return value.Format("2006-01-02T15:04:05.000000") + "Z"
}
