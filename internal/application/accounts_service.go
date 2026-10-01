package application

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"codex-lb/internal/domain"
)

const (
	defaultAccountEmail = "unknown@example.com"
	defaultAccountPlan  = "unknown"
	maxAccountAlias     = 255
)

var accountPlanTypes = map[string]bool{
	"free": true, "plus": true, "pro": true, "prolite": true, "team": true,
	"business": true, "enterprise": true, "edu": true,
}

var routingPolicies = map[string]bool{"normal": true, "burn_first": true, "preserve": true}

// OAuthError marks upstream OAuth protocol failures surfaced as HTTP 502.
type OAuthError struct {
	Code    string
	Message string
	Status  int
}

func (e *OAuthError) Error() string { return e.Message }

type OAuthTokens struct {
	AccessToken  string
	RefreshToken string
	IDToken      string
}

type DeviceCode struct {
	VerificationURL  string
	UserCode         string
	DeviceAuthID     string
	IntervalSeconds  int
	ExpiresInSeconds int
}

// OAuthClient is the ChatGPT OAuth protocol port; tests provide offline stubs.
type OAuthClient interface {
	AuthorizationURL(state, codeChallenge string) string
	ExchangeCode(ctx context.Context, code, codeVerifier string) (OAuthTokens, error)
	RequestDeviceCode(ctx context.Context) (DeviceCode, error)
	ExchangeDeviceToken(ctx context.Context, deviceAuthID, userCode string) (OAuthTokens, bool, error)
}

type AccountIdentityRepository interface {
	Accounts
	AccountPolicyRepository
	SaveAccountIdentity(context.Context, domain.Account, domain.AccountCredential) error
}

type AccountPolicyRepository interface {
	UpdateAccountAlias(context.Context, string, string) error
	UpdateAccountWarmup(context.Context, string, bool) error
	UpdateAccountRoutingPolicy(context.Context, string, string) error
	UpdateAccountSecurityAuthorization(context.Context, string, bool) error
	TransitionAccountStatus(context.Context, string, domain.AccountStatus, string, domain.AccountStatus) error
}

type AccountsService struct {
	accounts AccountIdentityRepository
	settings Settings
	cipher   SecretCipher
	oauth    OAuthClient
	now      func() time.Time
	flows    oauthFlowStore
	ctx      context.Context
	cancel   context.CancelFunc
	callback OAuthCallbackListener
}

func NewAccountsService(accounts AccountIdentityRepository, settings Settings, cipher SecretCipher, oauth OAuthClient, now func() time.Time) *AccountsService {
	if now == nil {
		now = time.Now
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &AccountsService{accounts: accounts, settings: settings, cipher: cipher, oauth: oauth, now: now, ctx: ctx, cancel: cancel}
}

type AccountImportResult struct {
	AccountID      string  `json:"accountId"`
	Email          string  `json:"email"`
	WorkspaceID    *string `json:"workspaceId"`
	WorkspaceLabel *string `json:"workspaceLabel"`
	SeatType       *string `json:"seatType"`
	PlanType       string  `json:"planType"`
	Status         string  `json:"status"`
}

func (s *AccountsService) visible(ctx context.Context, id string) (domain.Account, error) {
	account, err := s.accounts.GetAccount(ctx, id)
	if err != nil {
		return account, err
	}
	if deletedAccount(account) {
		return account, domain.ErrNotFound
	}
	return account, nil
}

func deletedAccount(account domain.Account) bool {
	return account.Status == domain.AccountDeactivated && account.DeactivationReason == "deleted"
}

func (s *AccountsService) PauseAccount(ctx context.Context, id string) error {
	account, err := s.visible(ctx, id)
	if err != nil {
		return err
	}
	if account.Status == domain.AccountReauthRequired || account.Status == domain.AccountDeactivated {
		return fmt.Errorf("pause %s from %s: %w", id, account.Status, domain.ErrConflict)
	}
	return s.accounts.TransitionAccountStatus(ctx, id, account.Status, account.DeactivationReason, domain.AccountPaused)
}

func (s *AccountsService) ReactivateAccount(ctx context.Context, id string) error {
	account, err := s.visible(ctx, id)
	if err != nil {
		return err
	}
	if account.Status == domain.AccountReauthRequired {
		return fmt.Errorf("reactivate %s from reauth_required: %w", id, domain.ErrConflict)
	}
	return s.accounts.TransitionAccountStatus(ctx, id, account.Status, account.DeactivationReason, domain.AccountActive)
}

func (s *AccountsService) SetAlias(ctx context.Context, id string, alias *string) (string, *string, error) {
	normalized := normalizeAlias(alias)
	if normalized != nil && len(*normalized) > maxAccountAlias {
		return "", nil, fmt.Errorf("alias length: %w", domain.ErrInvalid)
	}
	if err := s.accounts.UpdateAccountAlias(ctx, id, derefString(normalized)); err != nil {
		return "", nil, err
	}
	return id, normalized, nil
}

func (s *AccountsService) SetLimitWarmup(ctx context.Context, id string, enabled bool) error {
	return s.accounts.UpdateAccountWarmup(ctx, id, enabled)
}

func (s *AccountsService) SetRoutingPolicy(ctx context.Context, id, policy string) error {
	if !routingPolicies[policy] {
		return fmt.Errorf("routing policy %q: %w", policy, domain.ErrInvalid)
	}
	return s.accounts.UpdateAccountRoutingPolicy(ctx, id, policy)
}

func (s *AccountsService) UpdateAccount(ctx context.Context, id string, securityWorkAuthorized *bool) error {
	if securityWorkAuthorized == nil {
		return fmt.Errorf("empty account update: %w", domain.ErrInvalid)
	}
	return s.accounts.UpdateAccountSecurityAuthorization(ctx, id, *securityWorkAuthorized)
}

func (s *AccountsService) DeleteAccount(ctx context.Context, id string, deleteHistory bool) error {
	s.flows.mu.Lock()
	defer s.flows.mu.Unlock()
	// Login already in progress is not a new instruction to restore an account
	// deleted while its provider callback was outstanding. Other logins continue.
	for _, flow := range s.flows.flows {
		if flow.status != "pending" {
			continue
		}
		if flow.deletedAccounts == nil {
			flow.deletedAccounts = make(map[string]bool)
		}
		flow.deletedAccounts[id] = true
	}
	return s.accounts.DeleteAccount(ctx, id, deleteHistory)
}

type authFileTokens struct {
	IDToken      string  `json:"idToken"`
	AccessToken  string  `json:"accessToken"`
	RefreshToken string  `json:"refreshToken"`
	AccountID    *string `json:"accountId"`
}

type authFile struct {
	Tokens        authFileTokens  `json:"tokens"`
	LastRefreshAt json.RawMessage `json:"-"`
}

func (s *AccountsService) ImportAccount(ctx context.Context, raw []byte) (AccountImportResult, error) {
	auth, err := parseAuthFile(raw)
	if err != nil {
		return AccountImportResult{}, err
	}
	account, credential, err := s.accountFromTokens(auth.Tokens, "")
	if err != nil {
		return AccountImportResult{}, err
	}
	account, err = s.persistImportedAccount(ctx, account, credential)
	if err != nil {
		return AccountImportResult{}, err
	}
	saved, err := s.visible(ctx, account.ID)
	if err != nil {
		return AccountImportResult{}, err
	}
	return importResult(saved), nil
}

func (s *AccountsService) persistImportedAccount(ctx context.Context, account domain.Account, credential domain.AccountCredential) (domain.Account, error) {
	existing, err := s.accounts.GetAccount(ctx, account.ID)
	switch {
	case err == domain.ErrNotFound:
		settings, settingsErr := s.settings.LoadSettings(ctx)
		if settingsErr != nil {
			return account, settingsErr
		}
		if !settings.ImportWithoutOverwrite {
			merged, mergeErr := s.mergeByEmail(ctx, account)
			if mergeErr != nil {
				return account, mergeErr
			}
			account = merged
		}
	case err != nil:
		return account, err
	default:
		if deletedAccount(existing) {
			account.CreatedAt = time.Time{}
		} else {
			account.CreatedAt = existing.CreatedAt
		}
	}
	credential.AccountID = account.ID
	return account, s.accounts.SaveAccountIdentity(ctx, account, credential)
}

func (s *AccountsService) mergeByEmail(ctx context.Context, account domain.Account) (domain.Account, error) {
	all, err := s.accounts.ListAccounts(ctx)
	if err != nil {
		return account, err
	}
	var matches []domain.Account
	for _, candidate := range all {
		if candidate.Email == account.Email && !deletedAccount(candidate) {
			matches = append(matches, candidate)
		}
	}
	if len(matches) > 1 {
		return account, fmt.Errorf("multiple accounts match %s: %w", account.Email, domain.ErrConflict)
	}
	if len(matches) == 1 {
		account.ID = matches[0].ID
		account.CreatedAt = matches[0].CreatedAt
	}
	return account, nil
}

func importResult(account domain.Account) AccountImportResult {
	return AccountImportResult{
		AccountID:      account.ID,
		Email:          account.Email,
		WorkspaceID:    nullable(account.WorkspaceID),
		WorkspaceLabel: nullable(account.WorkspaceLabel),
		SeatType:       nullable(account.SeatType),
		PlanType:       account.PlanType,
		Status:         string(account.Status),
	}
}

// parseAuthFile resolves the supported token aliases before identity or storage changes.
func parseAuthFile(raw []byte) (authFile, error) {
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(raw, &parsed); err != nil || parsed["tokens"] == nil {
		return authFile{}, fmt.Errorf("auth.json: %w", domain.ErrInvalid)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(parsed["tokens"], &fields); err != nil {
		return authFile{}, fmt.Errorf("auth.json tokens: %w", domain.ErrInvalid)
	}
	var idToken, accessToken, refreshToken, accountID *string
	for _, field := range []struct {
		canonical, legacy string
		value             **string
	}{
		{"id_token", "idToken", &idToken},
		{"access_token", "accessToken", &accessToken},
		{"refresh_token", "refreshToken", &refreshToken},
		{"account_id", "accountId", &accountID},
	} {
		for _, name := range []string{field.canonical, field.legacy} {
			valueJSON, present := fields[name]
			if !present {
				continue
			}
			var value *string
			if err := json.Unmarshal(valueJSON, &value); err != nil {
				return authFile{}, fmt.Errorf("auth.json token type: %w", domain.ErrInvalid)
			}
			if value != nil {
				if *field.value != nil && **field.value != *value {
					return authFile{}, fmt.Errorf("auth.json conflicting token aliases: %w", domain.ErrInvalid)
				}
				*field.value = value
			}
		}
	}
	auth := authFile{Tokens: authFileTokens{
		IDToken: derefString(idToken), AccessToken: derefString(accessToken),
		RefreshToken: derefString(refreshToken), AccountID: accountID,
	}}
	if auth.Tokens.IDToken == "" || auth.Tokens.AccessToken == "" || auth.Tokens.RefreshToken == "" {
		return authFile{}, fmt.Errorf("auth.json token values: %w", domain.ErrInvalid)
	}
	auth.LastRefreshAt = parsed["lastRefreshAt"]
	if auth.LastRefreshAt == nil {
		auth.LastRefreshAt = parsed["last_refresh"]
	}
	return auth, nil
}

func normalizeAlias(alias *string) *string {
	if alias == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*alias)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func nullable(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
