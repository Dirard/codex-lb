// Package chatgpt implements the ChatGPT OAuth HTTP protocol used by the
// codex CLI: authorization-code + PKCE browser login and device-code login.
package chatgpt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"codex-lb/internal/application"
)

const (
	defaultAuthBaseURL = "https://auth.openai.com"
	defaultClientID    = "app_EMoamEEZ73f0CkXaXp7hrann"
	defaultOriginator  = "codex_chatgpt_desktop"
	defaultScope       = "openid profile email offline_access"
	defaultRedirectURI = "http://localhost:1455/auth/callback"
)

type OAuthConfig struct {
	BaseURL     string
	ClientID    string
	Originator  string
	Scope       string
	RedirectURI string
	Timeout     time.Duration
	HTTPClient  *http.Client
}

func DefaultOAuthConfig() OAuthConfig {
	return OAuthConfig{
		BaseURL:     defaultAuthBaseURL,
		ClientID:    defaultClientID,
		Originator:  defaultOriginator,
		Scope:       defaultScope,
		RedirectURI: defaultRedirectURI,
		Timeout:     30 * time.Second,
	}
}

type Client struct {
	config OAuthConfig
}

func NewOAuthClient(config OAuthConfig) *Client {
	if config.BaseURL == "" {
		config.BaseURL = defaultAuthBaseURL
	}
	if config.ClientID == "" {
		config.ClientID = defaultClientID
	}
	if config.Originator == "" {
		config.Originator = defaultOriginator
	}
	if config.Scope == "" {
		config.Scope = defaultScope
	}
	if config.RedirectURI == "" {
		config.RedirectURI = defaultRedirectURI
	}
	if config.Timeout == 0 {
		config.Timeout = 30 * time.Second
	}
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{Timeout: config.Timeout}
	}
	client := *config.HTTPClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	config.HTTPClient = &client
	return &Client{config: config}
}

func (c *Client) AuthorizationURL(state, codeChallenge string) string {
	query := url.Values{}
	query.Set("response_type", "code")
	query.Set("client_id", c.config.ClientID)
	query.Set("redirect_uri", c.config.RedirectURI)
	query.Set("scope", c.config.Scope)
	query.Set("code_challenge", codeChallenge)
	query.Set("code_challenge_method", "S256")
	query.Set("state", state)
	query.Set("id_token_add_organizations", "true")
	query.Set("codex_cli_simplified_flow", "true")
	query.Set("originator", c.config.Originator)
	return strings.TrimRight(c.config.BaseURL, "/") + "/oauth/authorize?" + query.Encode()
}

func (c *Client) ExchangeCode(ctx context.Context, code, codeVerifier string) (application.OAuthTokens, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", c.config.ClientID)
	form.Set("code", code)
	form.Set("code_verifier", codeVerifier)
	form.Set("redirect_uri", c.config.RedirectURI)
	var payload tokenPayload
	if err := c.postForm(ctx, "/oauth/token", form, &payload); err != nil {
		return application.OAuthTokens{}, err
	}
	return payload.tokens()
}

func (c *Client) RequestDeviceCode(ctx context.Context) (application.DeviceCode, error) {
	body, _ := json.Marshal(map[string]string{"client_id": c.config.ClientID})
	var payload devicePayload
	if err := c.postJSON(ctx, "/api/accounts/deviceauth/usercode", body, &payload); err != nil {
		return application.DeviceCode{}, err
	}
	userCode := firstNonEmpty(payload.UserCode, payload.UserCodeAlias)
	if userCode == "" || payload.DeviceAuthID == "" {
		return application.DeviceCode{}, oauthError("invalid_response", "Device auth response missing fields", 0)
	}
	expiresIn := payload.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = payload.expiresAtRemaining()
	}
	if expiresIn <= 0 {
		expiresIn = 900
	}
	return application.DeviceCode{
		VerificationURL:  strings.TrimRight(c.config.BaseURL, "/") + "/codex/device",
		UserCode:         userCode,
		DeviceAuthID:     payload.DeviceAuthID,
		IntervalSeconds:  max(payload.Interval, 0),
		ExpiresInSeconds: expiresIn,
	}, nil
}

func (c *Client) ExchangeDeviceToken(ctx context.Context, deviceAuthID, userCode string) (application.OAuthTokens, bool, error) {
	body, _ := json.Marshal(map[string]string{"device_auth_id": deviceAuthID, "user_code": userCode})
	var payload tokenPayload
	if err := c.postJSON(ctx, "/api/accounts/deviceauth/token", body, &payload); err != nil {
		var oauthErr *application.OAuthError
		if errors.As(err, &oauthErr) && (oauthErr.Code == "authorization_pending" || oauthErr.Code == "slow_down") {
			return application.OAuthTokens{}, false, nil
		}
		if errors.As(err, &oauthErr) && (oauthErr.Status == http.StatusForbidden || oauthErr.Status == http.StatusNotFound) {
			return application.OAuthTokens{}, false, nil
		}
		return application.OAuthTokens{}, false, err
	}
	tokens, err := payload.tokens()
	if err != nil {
		return application.OAuthTokens{}, false, err
	}
	return tokens, true, nil
}

func (c *Client) postForm(ctx context.Context, path string, form url.Values, out *tokenPayload) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.config.BaseURL, "/")+path, strings.NewReader(form.Encode()))
	if err != nil {
		return oauthError("invalid_request", "OAuth request could not be built", 0)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.do(req, out)
}

func (c *Client) postJSON(ctx context.Context, path string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.config.BaseURL, "/")+path, strings.NewReader(string(body)))
	if err != nil {
		return oauthError("invalid_request", "OAuth request could not be built", 0)
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, out)
}

func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.config.HTTPClient.Do(req)
	if err != nil {
		return oauthError("network_error", "OAuth request failed", 0)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return oauthError("network_error", "OAuth response could not be read", 0)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		if resp.StatusCode >= 400 {
			return errorFromStatus(resp.StatusCode, raw)
		}
		return oauthError("invalid_response", "OAuth response invalid", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return errorFromStatus(resp.StatusCode, raw)
	}
	return nil
}

type tokenPayload struct {
	AccessToken        string          `json:"access_token"`
	RefreshToken       string          `json:"refresh_token"`
	IDToken            string          `json:"id_token"`
	Error              json.RawMessage `json:"error"`
	ErrorCode          string          `json:"error_code"`
	Code               string          `json:"code"`
	Status             string          `json:"status"`
	DeviceAuthID       string          `json:"device_auth_id"`
	UserCode           string          `json:"user_code"`
	UserCodeAlias      string          `json:"usercode"`
	Interval           int             `json:"interval"`
	ExpiresIn          int             `json:"expires_in"`
	ExpiresAt          string          `json:"expires_at"`
	dashboardErrorCode string
}

func (p *tokenPayload) tokens() (application.OAuthTokens, error) {
	if p.AccessToken == "" || p.RefreshToken == "" || p.IDToken == "" {
		return application.OAuthTokens{}, oauthError("invalid_response", "OAuth response missing tokens", 0)
	}
	return application.OAuthTokens{AccessToken: p.AccessToken, RefreshToken: p.RefreshToken, IDToken: p.IDToken}, nil
}

func (p *tokenPayload) errorCode() string {
	if len(p.Error) > 0 {
		var nested struct {
			Code  string `json:"code"`
			Error string `json:"error"`
		}
		if json.Unmarshal(p.Error, &nested) == nil && (nested.Code != "" || nested.Error != "") {
			if nested.Code != "" {
				return nested.Code
			}
			return nested.Error
		}
		var message string
		if json.Unmarshal(p.Error, &message) == nil && message != "" {
			return message
		}
	}
	return firstNonEmpty(p.ErrorCode, p.Code)
}

func (p *tokenPayload) expiresAtRemaining() int {
	if p.ExpiresAt == "" {
		return 0
	}
	expires, err := time.Parse(time.RFC3339, strings.ReplaceAll(p.ExpiresAt, "Z", "+00:00"))
	if err != nil {
		return 0
	}
	if delta := int(time.Until(expires).Seconds()); delta > 0 {
		return delta
	}
	return 0
}

type devicePayload = tokenPayload

func errorFromStatus(status int, raw []byte) *application.OAuthError {
	payload := tokenPayload{dashboardErrorCode: ""}
	_ = json.Unmarshal(raw, &payload)
	return providerOAuthError(status, payload.errorCode())
}

func providerOAuthError(status int, code string) *application.OAuthError {
	message := "OAuth request was rejected; retry sign-in"
	switch code {
	case "authorization_pending":
		message = "Waiting for account authorization"
	case "slow_down":
		message = "Authorization is pending; polling must wait"
	case "invalid_grant", "invalid_refresh_token", "refresh_token_expired", "refresh_token_reused", "refresh_token_invalidated", "token_expired":
		message = "The OAuth grant is no longer valid; sign in again"
	case "account_deactivated":
		message = "The upstream account is deactivated"
	case "access_denied":
		message = "Account authorization was denied"
	case "expired_token":
		message = "The authorization attempt expired; start sign-in again"
	case "invalid_client", "invalid_request":
		message = "The OAuth service rejected the sign-in request"
	default:
		code = fmt.Sprintf("http_%d", status)
	}
	// Never include arbitrary provider text: some failures echo the submitted
	// grant, refresh token, or form body in their error_description/message.
	return oauthError(code, message, status)
}

func oauthError(code, message string, status int) *application.OAuthError {
	return &application.OAuthError{Code: code, Message: message, Status: status}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
