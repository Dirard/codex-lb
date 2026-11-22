// Package antigravity talks to Google's OAuth endpoints and the Cloud Code
// v1internal control methods as the Google Antigravity client, for signing in
// Antigravity accounts and listing their models. Model requests themselves go
// through the upstream adapter.
package antigravity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

const (
	// ClientID and clientSecret are Google's published installed-app client
	// of the Antigravity apps. An installed-app secret is not confidential;
	// it is kept in code, not configuration. The owner accepted on 2026-11-22
	// that Google's Antigravity Terms treat third-party access as a breach.
	ClientID     = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	clientSecret = "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"
	// RedirectURI is Google's hosted page that shows the code to paste back.
	RedirectURI = "https://antigravity.google/oauth-callback"

	DefaultAuthURL     = "https://accounts.google.com/o/oauth2/v2/auth"
	DefaultTokenURL    = "https://oauth2.googleapis.com/token"
	DefaultUserInfoURL = "https://www.googleapis.com/oauth2/v2/userinfo"
	// DefaultClientVersion is the Antigravity client version presented in the
	// User-Agent unless CODEX_LB_ANTIGRAVITY_CLIENT_VERSION says otherwise.
	DefaultClientVersion = domain.DefaultAntigravityClientVersion

	maxTokenBytes     = 1 << 20
	maxCloudCodeBytes = 4 << 20
	maxModels         = 256
	callTimeout       = 30 * time.Second
	onboardAttempts   = 5
	onboardInterval   = 2 * time.Second
)

// Scopes are the Antigravity Hub 2.34.0 scopes, in its order.
var Scopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
	"https://www.googleapis.com/auth/cclog",
	"https://www.googleapis.com/auth/experimentsandconfigs",
	"https://www.googleapis.com/auth/aicode",
}

// ValidClientVersion accepts a dotted version such as 2.34.0.
func ValidClientVersion(version string) bool { return domain.ValidAntigravityClientVersion(version) }

// UserAgent is the User-Agent of every Cloud Code call: the Antigravity Hub
// form with this host's operating system and architecture, without a
// changelist or any other client fingerprint.
func UserAgent(version string) string {
	if !ValidClientVersion(version) {
		version = DefaultClientVersion
	}
	return fmt.Sprintf("antigravity/hub/%s (aidev_client; os_type=windows; arch=amd64; cl=1023160026)", version)
}

// Config sets the HTTP client, how the presented client version is read
// (the runtime setting; nil means the default version) and, for tests,
// every endpoint and the onboarding poll interval.
type Config struct {
	HTTPClient           *http.Client
	ResolveClientVersion func(context.Context) (string, error)
	AuthURL              string
	TokenURL             string
	UserInfoURL          string
	CloudCodeBaseURL     string
	OnboardInterval      time.Duration
}

type Client struct {
	http            *http.Client
	resolveVersion  func(context.Context) (string, error)
	authURL         string
	tokenURL        string
	userInfoURL     string
	cloudCode       string
	onboardInterval time.Duration
}

var _ application.AntigravityUpstream = (*Client)(nil)

func New(config Config) *Client {
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{}
	}
	owned := *client
	// Tokens and codes must not follow a redirect to another host.
	owned.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c := &Client{http: &owned, resolveVersion: config.ResolveClientVersion, authURL: config.AuthURL, tokenURL: config.TokenURL,
		userInfoURL: config.UserInfoURL, cloudCode: strings.TrimSuffix(config.CloudCodeBaseURL, "/"), onboardInterval: config.OnboardInterval}
	if c.authURL == "" {
		c.authURL = DefaultAuthURL
	}
	if c.tokenURL == "" {
		c.tokenURL = DefaultTokenURL
	}
	if c.userInfoURL == "" {
		c.userInfoURL = DefaultUserInfoURL
	}
	if c.cloudCode == "" {
		c.cloudCode = domain.AntigravityBaseURL
	}
	if c.onboardInterval <= 0 {
		c.onboardInterval = onboardInterval
	}
	return c
}

// AuthorizationURL is Google's consent page for the Antigravity client with
// PKCE S256, offline access and the hosted paste-code redirect.
func (c *Client) AuthorizationURL(state, codeChallenge string) string {
	query := url.Values{}
	query.Set("client_id", ClientID)
	query.Set("redirect_uri", RedirectURI)
	query.Set("response_type", "code")
	query.Set("scope", strings.Join(Scopes, " "))
	query.Set("access_type", "offline")
	query.Set("prompt", "consent")
	query.Set("state", state)
	query.Set("code_challenge", codeChallenge)
	query.Set("code_challenge_method", "S256")
	return c.authURL + "?" + query.Encode()
}

// ExchangeCode trades a pasted code for tokens. A refused code is
// application.ErrAntigravityGrantRefused.
func (c *Client) ExchangeCode(ctx context.Context, code, codeVerifier string) (application.AntigravityTokenSet, error) {
	return c.token(ctx, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {codeVerifier},
		"redirect_uri": {RedirectURI}, "client_id": {ClientID}, "client_secret": {clientSecret}})
}

// Refresh renews the access token. A refused refresh token (invalid_grant) is
// application.ErrAntigravityGrantRefused; anything else is transient.
func (c *Client) Refresh(ctx context.Context, refreshToken string) (application.AntigravityTokenSet, error) {
	return c.token(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refreshToken},
		"client_id": {ClientID}, "client_secret": {clientSecret}})
}

func (c *Client) token(ctx context.Context, form url.Values) (application.AntigravityTokenSet, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return application.AntigravityTokenSet{}, errors.New("invalid google token request")
	}
	agent, err := c.userAgent(ctx)
	if err != nil {
		return application.AntigravityTokenSet{}, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("User-Agent", agent)
	status, body, err := c.do(request, maxTokenBytes)
	if err != nil {
		return application.AntigravityTokenSet{}, err
	}
	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Error        string `json:"error"`
	}
	_ = json.Unmarshal(body, &payload)
	if status == http.StatusBadRequest || status == http.StatusUnauthorized {
		if payload.Error == "invalid_grant" {
			return application.AntigravityTokenSet{}, application.ErrAntigravityGrantRefused
		}
	}
	if status != http.StatusOK {
		// Google's error description can quote the request; only the status
		// and the error code leave this function.
		return application.AntigravityTokenSet{}, fmt.Errorf("google token endpoint status %d (%s)", status, safeCode(payload.Error))
	}
	if payload.AccessToken == "" {
		return application.AntigravityTokenSet{}, errors.New("google token endpoint answered without an access token")
	}
	return application.AntigravityTokenSet{Access: payload.AccessToken, Refresh: payload.RefreshToken,
		ExpiresIn: time.Duration(max(payload.ExpiresIn, 0)) * time.Second}, nil
}

// UserEmail reads the signed-in account's email from userinfo.
func (c *Client) UserEmail(ctx context.Context, accessToken string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.userInfoURL, nil)
	if err != nil {
		return "", errors.New("invalid google userinfo request")
	}
	agent, err := c.userAgent(ctx)
	if err != nil {
		return "", err
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("User-Agent", agent)
	status, body, err := c.do(request, maxTokenBytes)
	if err != nil {
		return "", err
	}
	if status == http.StatusUnauthorized {
		return "", application.ErrAntigravityUnauthorized
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("google userinfo status %d", status)
	}
	var payload struct {
		Email string `json:"email"`
	}
	if json.Unmarshal(body, &payload) != nil || strings.TrimSpace(payload.Email) == "" {
		return "", errors.New("google userinfo answered without an email")
	}
	return strings.TrimSpace(payload.Email), nil
}

type cloudCodeTier struct {
	ID        string `json:"id"`
	IsDefault bool   `json:"isDefault"`
}

// LoadCodeAssist reads the account's Cloud Code project and tier: the paid
// tier when there is one, else the current tier.
func (c *Client) LoadCodeAssist(ctx context.Context, accessToken string) (application.AntigravityCodeAssist, error) {
	var payload struct {
		Project      json.RawMessage `json:"cloudaicompanionProject"`
		CurrentTier  *cloudCodeTier  `json:"currentTier"`
		PaidTier     *cloudCodeTier  `json:"paidTier"`
		AllowedTiers []cloudCodeTier `json:"allowedTiers"`
	}
	body := map[string]any{"metadata": map[string]string{"ideType": "ANTIGRAVITY"}}
	if err := c.call(ctx, accessToken, "loadCodeAssist", body, &payload); err != nil {
		return application.AntigravityCodeAssist{}, err
	}
	assist := application.AntigravityCodeAssist{Project: projectID(payload.Project)}
	switch {
	case payload.PaidTier != nil && payload.PaidTier.ID != "":
		assist.Tier = payload.PaidTier.ID
	case payload.CurrentTier != nil:
		assist.Tier = payload.CurrentTier.ID
	}
	for _, tier := range payload.AllowedTiers {
		if tier.IsDefault && tier.ID != "" {
			assist.OnboardTier = tier.ID
			break
		}
	}
	return assist, nil
}

// OnboardUser starts onboarding and asks again, a bounded number of times,
// until the long-running operation is done and names the project.
func (c *Client) OnboardUser(ctx context.Context, accessToken, tierID string) (string, error) {
	body := map[string]any{"tierId": tierID, "metadata": map[string]string{"ideType": "ANTIGRAVITY"}}
	for attempt := 0; attempt < onboardAttempts; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(c.onboardInterval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return "", ctx.Err()
			case <-timer.C:
			}
		}
		var operation struct {
			Done     bool `json:"done"`
			Response struct {
				Project json.RawMessage `json:"cloudaicompanionProject"`
			} `json:"response"`
		}
		if err := c.call(ctx, accessToken, "onboardUser", body, &operation); err != nil {
			return "", err
		}
		if operation.Done {
			if project := projectID(operation.Response.Project); project != "" {
				return project, nil
			}
			return "", errors.New("cloud code onboarding finished without a project")
		}
	}
	return "", errors.New("cloud code onboarding did not finish")
}

// flexInt reads a proto int64, which JSON may carry as a number or a string.
type flexInt int64

func (value *flexInt) UnmarshalJSON(raw []byte) error {
	text := strings.Trim(string(raw), `"`)
	if text == "" || text == "null" {
		return nil
	}
	parsed, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return nil // An unusable limit is treated as unknown.
	}
	*value = flexInt(parsed)
	return nil
}

// AvailableModels lists the account's models, internal and image-generation
// ids left out, ordered by id.
func (c *Client) AvailableModels(ctx context.Context, accessToken, project string) ([]application.AntigravityModel, error) {
	var payload struct {
		Models map[string]struct {
			DisplayName      string  `json:"displayName"`
			MaxTokens        flexInt `json:"maxTokens"`
			MaxOutputTokens  flexInt `json:"maxOutputTokens"`
			SupportsImages   bool    `json:"supportsImages"`
			SupportsThinking bool    `json:"supportsThinking"`
			Disabled         bool    `json:"disabled"`
		} `json:"models"`
		ImageGenerationModelIDs []string `json:"imageGenerationModelIds"`
	}
	body := map[string]string{}
	if project != "" {
		body["project"] = project
	}
	if err := c.call(ctx, accessToken, "fetchAvailableModels", body, &payload); err != nil {
		return nil, err
	}
	imageGeneration := make(map[string]bool, len(payload.ImageGenerationModelIDs))
	for _, id := range payload.ImageGenerationModelIDs {
		imageGeneration[id] = true
	}
	models := make([]application.AntigravityModel, 0, len(payload.Models))
	for id, model := range payload.Models {
		if imageGeneration[id] || internalModel(id) {
			continue
		}
		models = append(models, application.AntigravityModel{
			ID: id, DisplayName: model.DisplayName, ContextWindow: int64(model.MaxTokens), MaxOutputTokens: int64(model.MaxOutputTokens),
			Images: model.SupportsImages, Thinking: model.SupportsThinking, Disabled: model.Disabled,
		})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	if len(models) > maxModels {
		models = models[:maxModels]
	}
	return models, nil
}

// internalModel names ids the Antigravity apps use for completion, routing
// and image generation, which no Codex request should reach.
func internalModel(id string) bool {
	id = strings.TrimSpace(id)
	return id == "" || len(id) > 255 || strings.HasPrefix(id, "tab_") || strings.HasPrefix(id, "chat_") ||
		id == "gemini-pro-agent" || strings.HasPrefix(id, "gemini-pro-orchestrator") || strings.Contains(id, "-image")
}

// call posts one Cloud Code control method with only Content-Type,
// Authorization and User-Agent headers.
// userAgent builds the User-Agent of one call from the current client
// version setting, so a saved version applies without restart.
func (c *Client) userAgent(ctx context.Context) (string, error) {
	if c.resolveVersion == nil {
		return UserAgent(DefaultClientVersion), nil
	}
	version, err := c.resolveVersion(ctx)
	if err != nil || !ValidClientVersion(version) {
		return "", errors.New("antigravity client version unavailable")
	}
	return UserAgent(version), nil
}

func (c *Client) call(ctx context.Context, accessToken, method string, body, out any) error {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cloudCode+"/v1internal:"+method, bytes.NewReader(encoded))
	if err != nil {
		return errors.New("invalid cloud code request")
	}
	agent, err := c.userAgent(ctx)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("User-Agent", agent)
	status, payload, err := c.do(request, maxCloudCodeBytes)
	if err != nil {
		return err
	}
	if status == http.StatusUnauthorized {
		return application.ErrAntigravityUnauthorized
	}
	if status != http.StatusOK {
		return fmt.Errorf("cloud code %s status %d", method, status)
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("cloud code %s answered with invalid JSON", method)
	}
	return nil
}

// do sends a request and reads at most limit bytes of the answer. Transport
// errors are replaced, because they can quote a request that holds a token.
func (c *Client) do(request *http.Request, limit int64) (int, []byte, error) {
	response, err := c.http.Do(request)
	if err != nil {
		if ctxErr := request.Context().Err(); ctxErr != nil {
			return 0, nil, ctxErr
		}
		return 0, nil, fmt.Errorf("google endpoint %s is unreachable", request.URL.Host)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return 0, nil, errors.New("google response could not be read")
	}
	if int64(len(body)) > limit {
		return 0, nil, errors.New("google response exceeds the bound")
	}
	return response.StatusCode, body, nil
}

// projectID reads cloudaicompanionProject, a string or an object with an id.
func projectID(raw json.RawMessage) string {
	var name string
	if json.Unmarshal(raw, &name) == nil {
		return strings.TrimSpace(name)
	}
	var object struct {
		ID        string `json:"id"`
		ProjectID string `json:"projectId"`
	}
	if json.Unmarshal(raw, &object) == nil {
		if object.ID != "" {
			return strings.TrimSpace(object.ID)
		}
		return strings.TrimSpace(object.ProjectID)
	}
	return ""
}

// safeCode keeps an OAuth error code only when it is a plain identifier.
func safeCode(code string) string {
	if code == "" || len(code) > 64 || strings.Trim(code, "abcdefghijklmnopqrstuvwxyz_") != "" {
		return "unknown"
	}
	return code
}
