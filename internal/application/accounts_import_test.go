package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"codex-lb/internal/domain"
)

func TestParseAuthFileTokenNames(t *testing.T) {
	for _, style := range []string{"codex", "dashboard", "mixed", "both", "null_aliases"} {
		t.Run(style, func(t *testing.T) {
			fields := map[string]any{}
			for i, field := range []struct{ canonical, legacy, value string }{
				{"id_token", "idToken", "synthetic-id-token"},
				{"access_token", "accessToken", "synthetic-access-token"},
				{"refresh_token", "refreshToken", "synthetic-refresh-token"},
				{"account_id", "accountId", "synthetic-account-id"},
			} {
				switch style {
				case "codex":
					fields[field.canonical] = field.value
				case "dashboard":
					fields[field.legacy] = field.value
				case "mixed":
					fields[[]string{field.canonical, field.legacy}[i%2]] = field.value
				case "both":
					fields[field.canonical], fields[field.legacy] = field.value, field.value
				case "null_aliases":
					fields[field.canonical], fields[field.legacy] = nil, field.value
				}
			}
			raw, err := json.Marshal(map[string]any{"tokens": fields, "auth_mode": "chatgpt", "OPENAI_API_KEY": nil})
			if err != nil {
				t.Fatal(err)
			}
			auth, err := parseAuthFile(raw)
			if err != nil {
				t.Fatal(err)
			}
			if auth.Tokens.IDToken != "synthetic-id-token" || auth.Tokens.AccessToken != "synthetic-access-token" ||
				auth.Tokens.RefreshToken != "synthetic-refresh-token" || derefString(auth.Tokens.AccountID) != "synthetic-account-id" {
				t.Fatal("token aliases did not preserve values")
			}
		})
	}
}

func TestImportAccountRejectsInvalidTokenAliases(t *testing.T) {
	service, store, _, _ := newTestService(t, true)
	for _, input := range []string{
		`{"tokens":null}`, `{"tokens":[]}`, `{"tokens":{}}`,
		`{"tokens":{"id_token":null,"access_token":"a","refresh_token":"r"}}`,
		`{"tokens":{"id_token":"id","access_token":"","refresh_token":"r"}}`,
		`{"tokens":{"id_token":"id","access_token":"a"}}`,
		`{"tokens":{"id_token":1,"access_token":"a","refresh_token":"r"}}`,
		`{"tokens":{"id_token":"id","accessToken":false,"refresh_token":"r"}}`,
		`{"tokens":{"id_token":"id","access_token":"a","refresh_token":"r","account_id":[]}}`,
		`{"tokens":{"id_token":"id","access_token":"a","refresh_token":"r","idToken":"synthetic-sensitive-value"}}`,
		`{"tokens":{"id_token":"id","access_token":"a","refresh_token":"r","accessToken":"synthetic-sensitive-value"}}`,
		`{"tokens":{"id_token":"id","access_token":"a","refresh_token":"r","refreshToken":"synthetic-sensitive-value"}}`,
		`{"tokens":{"id_token":"id","access_token":"a","refresh_token":"r","account_id":"one","accountId":"synthetic-sensitive-value"}}`,
	} {
		if _, err := service.ImportAccount(context.Background(), []byte(input)); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("invalid token document was not rejected: %v", err)
		} else if strings.Contains(err.Error(), "synthetic-sensitive-value") {
			t.Fatal("import error exposed credential content")
		}
	}
	accounts, err := store.ListAccounts(context.Background())
	if err != nil || len(accounts) != 0 {
		t.Fatal("invalid imports modified account storage")
	}
}

func TestImportAccountCodexExportRoundTrip(t *testing.T) {
	ctx := context.Background()
	service, store, _, _ := newTestService(t, true)
	tokens := tokenSet("user-roundtrip")
	accountID := "explicit-chatgpt-account"
	for _, explicitID := range []*string{nil, &accountID} {
		raw, err := json.Marshal(codexAuthJSON{AuthMode: "chatgpt", Tokens: codexAuthTokensJSON{
			IDToken: tokens.IDToken, AccessToken: tokens.AccessToken, RefreshToken: tokens.RefreshToken, AccountID: explicitID,
		}})
		if err != nil {
			t.Fatal(err)
		}
		imported, err := service.ImportAccount(ctx, raw)
		if err != nil {
			t.Fatal(err)
		}
		account, err := store.GetAccount(ctx, imported.AccountID)
		if err != nil || explicitID != nil && account.ChatGPTAccountID != *explicitID || explicitID == nil && account.ChatGPTAccountID != "chatgpt-1" {
			t.Fatal("Codex account ID was not preserved or inferred from claims")
		}
		exported, err := service.ExportAuth(ctx, imported.AccountID)
		if err != nil {
			t.Fatal(err)
		}
		if exported.Tokens.IDToken != tokens.IDToken || exported.Tokens.AccessToken != tokens.AccessToken || exported.Tokens.RefreshToken != tokens.RefreshToken {
			t.Fatal("encrypted token round trip changed credentials")
		}
		raw, err = json.Marshal(exported.CodexAuthJSON)
		if err != nil {
			t.Fatal(err)
		}
		again, err := service.ImportAccount(ctx, raw)
		if err != nil || again.AccountID != imported.AccountID {
			t.Fatalf("Codex export reimport did not preserve identity: %v", err)
		}
	}
	accounts, err := store.ListAccounts(ctx)
	if err != nil || len(accounts) != 2 {
		t.Fatal("Codex export reimport created duplicate accounts")
	}
}
