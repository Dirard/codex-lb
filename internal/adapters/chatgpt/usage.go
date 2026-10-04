package chatgpt

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"codex-lb/internal/application"
)

const defaultUsageBaseURL = "https://chatgpt.com"

type UsageConfig struct {
	BaseURL    string
	Timeout    time.Duration
	HTTPClient *http.Client
}

// UsageClient calls the ChatGPT backend-api usage and reset-credit endpoints.
type UsageClient struct {
	config UsageConfig
}

func NewUsageClient(config UsageConfig) *UsageClient {
	if config.BaseURL == "" {
		config.BaseURL = defaultUsageBaseURL
	}
	if config.Timeout == 0 {
		config.Timeout = 30 * time.Second
	}
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{Timeout: config.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}
	}
	return &UsageClient{config: config}
}

func (c *UsageClient) FetchUsage(ctx context.Context, accessToken, chatgptAccountID string) (application.UsageSnapshot, error) {
	var payload usagePayload
	if err := c.get(ctx, accessToken, chatgptAccountID, "/wham/usage", &payload); err != nil {
		return application.UsageSnapshot{}, err
	}
	return payload.snapshot(), nil
}

func (c *UsageClient) FetchResetCredits(ctx context.Context, accessToken, chatgptAccountID string) (application.ResetCredits, error) {
	var payload struct {
		Credits        []resetCreditPayload `json:"credits"`
		AvailableCount *int                 `json:"available_count"`
	}
	if err := c.get(ctx, accessToken, chatgptAccountID, "/wham/rate-limit-reset-credits", &payload); err != nil {
		return application.ResetCredits{}, err
	}
	if payload.AvailableCount == nil || *payload.AvailableCount < 0 || payload.Credits == nil {
		return application.ResetCredits{}, usageError("invalid_response", "Invalid reset credits payload", 0)
	}
	credits := make([]application.ResetCredit, 0, len(payload.Credits))
	for _, credit := range payload.Credits {
		if strings.TrimSpace(credit.ID) == "" {
			return application.ResetCredits{}, usageError("invalid_response", "Invalid reset credit identity", 0)
		}
		credits = append(credits, credit.value())
	}
	return application.ResetCredits{AvailableCount: *payload.AvailableCount, Credits: credits}, nil
}

func (c *UsageClient) ConsumeResetCredit(ctx context.Context, accessToken, chatgptAccountID, creditID, redeemRequestID string) (application.ResetCreditConsume, error) {
	payloadBody := map[string]string{"redeem_request_id": redeemRequestID}
	if creditID != "" {
		payloadBody["credit_id"] = creditID
	}
	body, _ := json.Marshal(payloadBody)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url("/wham/rate-limit-reset-credits/consume"), strings.NewReader(string(body)))
	if err != nil {
		return application.ResetCreditConsume{}, usageError("invalid_request", "usage request could not be built", 0)
	}
	c.headers(req, accessToken, chatgptAccountID)
	req.Header.Set("Content-Type", "application/json")
	var payload consumePayload
	if err := c.do(req, &payload); err != nil {
		return application.ResetCreditConsume{}, err
	}
	// Consume is a non-idempotent upstream mutation: never retry it here.
	switch payload.Code {
	case "reset", "nothing_to_reset", "no_credit", "already_redeemed":
	default:
		return application.ResetCreditConsume{}, usageError("invalid_response", "Invalid reset credits consume payload", 0)
	}
	credit := payload.Credit.value()
	return application.ResetCreditConsume{
		Code: payload.Code, WindowsReset: payload.WindowsReset, RedeemedAt: credit.RedeemedAt,
	}, nil
}

func (c *UsageClient) get(ctx context.Context, accessToken, chatgptAccountID, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url(path), nil)
	if err != nil {
		return usageError("invalid_request", "usage request could not be built", 0)
	}
	c.headers(req, accessToken, chatgptAccountID)
	return c.do(req, out)
}

func (c *UsageClient) url(path string) string {
	base := strings.TrimRight(c.config.BaseURL, "/")
	if !strings.Contains(base, "/backend-api") {
		base += "/backend-api"
	}
	return base + path
}

func (c *UsageClient) headers(req *http.Request, accessToken, chatgptAccountID string) {
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	// Synthetic email_/local_ ids are not accepted upstream as account ids.
	if chatgptAccountID != "" && !strings.HasPrefix(chatgptAccountID, "email_") && !strings.HasPrefix(chatgptAccountID, "local_") {
		req.Header.Set("chatgpt-account-id", chatgptAccountID)
	}
}

func (c *UsageClient) do(req *http.Request, out any) error {
	resp, err := c.config.HTTPClient.Do(req)
	if err != nil {
		return usageError("network_error", "usage request failed", 0)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return usageError("network_error", "usage response could not be read", 0)
	}
	if resp.StatusCode >= 400 {
		return usageErrorFromResponse(resp.StatusCode, raw)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return usageError("invalid_response", "usage response invalid", resp.StatusCode)
	}
	return nil
}

func usageErrorFromResponse(status int, raw []byte) *application.UsageError {
	var envelope struct {
		Error struct {
			Code             string `json:"code"`
			Message          string `json:"message"`
			ErrorDescription string `json:"error_description"`
		} `json:"error"`
		ErrorDescription string `json:"error_description"`
		Message          string `json:"message"`
	}
	_ = json.Unmarshal(raw, &envelope)
	code := strings.ToLower(strings.TrimSpace(envelope.Error.Code))
	message := firstNonEmpty(envelope.Error.Message, envelope.Error.ErrorDescription, envelope.ErrorDescription, envelope.Message)
	if message == "" {
		message = fmt.Sprintf("usage request failed (%d)", status)
	}
	return usageError(code, message, status)
}

func usageError(code, message string, status int) *application.UsageError {
	return &application.UsageError{Code: code, Message: message, Status: status}
}

type usageWindowPayload struct {
	UsedPercent        *float64 `json:"used_percent"`
	ResetAt            *int64   `json:"reset_at"`
	LimitWindowSeconds *int64   `json:"limit_window_seconds"`
}

type usageCreditsPayload struct {
	Has       *bool   `json:"has_credits"`
	Unlimited *bool   `json:"unlimited"`
	Balance   *string `json:"balance"`
}

type additionalRateLimitPayload struct {
	LimitName      string            `json:"limit_name"`
	MeteredFeature string            `json:"metered_feature"`
	RateLimit      *rateLimitPayload `json:"rate_limit"`
}

type rateLimitPayload struct {
	Allowed         *bool               `json:"allowed"`
	LimitReached    *bool               `json:"limit_reached"`
	PrimaryWindow   *usageWindowPayload `json:"primary_window"`
	SecondaryWindow *usageWindowPayload `json:"secondary_window"`
}

type usagePayload struct {
	PlanType              string               `json:"plan_type"`
	WorkspaceID           string               `json:"workspace_id"`
	WorkspaceLabel        string               `json:"workspace_label"`
	SeatType              string               `json:"seat_type"`
	RateLimit             *rateLimitPayload    `json:"rate_limit"`
	Credits               *usageCreditsPayload `json:"credits"`
	RateLimitResetCredits *struct {
		AvailableCount *int `json:"available_count"`
	} `json:"rate_limit_reset_credits"`
	AdditionalRateLimits []additionalRateLimitPayload `json:"additional_rate_limits"`
}

func (p usagePayload) snapshot() application.UsageSnapshot {
	snapshot := application.UsageSnapshot{
		PlanType: p.PlanType, WorkspaceID: p.WorkspaceID,
		WorkspaceLabel: p.WorkspaceLabel, SeatType: p.SeatType,
	}
	if p.RateLimit != nil {
		snapshot.RateLimitAllowed, snapshot.RateLimitReached = p.RateLimit.Allowed, p.RateLimit.LimitReached
		snapshot.Primary, snapshot.Secondary = usageWindow(p.RateLimit.PrimaryWindow), usageWindow(p.RateLimit.SecondaryWindow)
		// Upstream can put a lone weekly allowance in primary_window.
		if snapshot.Primary != nil && snapshot.Secondary == nil && p.RateLimit.PrimaryWindow.LimitWindowSeconds != nil &&
			*p.RateLimit.PrimaryWindow.LimitWindowSeconds == 7*24*60*60 {
			snapshot.Secondary, snapshot.Primary = snapshot.Primary, nil
		}
		// Legacy normalization: a lone 30-day window reported as primary is
		// the monthly window (free plans), not a 5-hour primary quota.
		if snapshot.Primary != nil && snapshot.Secondary == nil && monthlyWindowMinutes(snapshot.Primary) {
			snapshot.Monthly, snapshot.Primary = snapshot.Primary, nil
		}
	}
	if p.Credits != nil {
		snapshot.Credits = application.UsageCredits{Has: p.Credits.Has, Unlimited: p.Credits.Unlimited, Balance: p.Credits.Balance}
	}
	if p.RateLimitResetCredits != nil {
		snapshot.ResetCreditCount = p.RateLimitResetCredits.AvailableCount
	}
	if p.AdditionalRateLimits != nil {
		snapshot.AdditionalQuotas = make([]application.AdditionalQuota, 0, len(p.AdditionalRateLimits))
	}
	for _, additional := range p.AdditionalRateLimits {
		quota := application.AdditionalQuota{LimitName: additional.LimitName, MeteredFeature: additional.MeteredFeature}
		if additional.RateLimit != nil {
			quota.Primary, quota.Secondary = usageWindow(additional.RateLimit.PrimaryWindow), usageWindow(additional.RateLimit.SecondaryWindow)
		}
		snapshot.AdditionalQuotas = append(snapshot.AdditionalQuotas, quota)
	}
	return snapshot
}

func monthlyWindowMinutes(window *application.UsageWindow) bool {
	return window.WindowMinutes != nil && *window.WindowMinutes == 30*24*60
}

func usageWindow(payload *usageWindowPayload) *application.UsageWindow {
	if payload == nil {
		return nil
	}
	window := application.UsageWindow{}
	if payload.UsedPercent != nil {
		used := *payload.UsedPercent
		if used < 0 {
			used = 0
		}
		if used > 100 {
			used = 100
		}
		window.UsedPercent = &used
	}
	if payload.ResetAt != nil && *payload.ResetAt > 0 {
		reset := time.Unix(*payload.ResetAt, 0).UTC()
		window.ResetAt = &reset
	}
	if payload.LimitWindowSeconds != nil && *payload.LimitWindowSeconds > 0 {
		minutes := int(*payload.LimitWindowSeconds / 60)
		window.WindowMinutes = &minutes
	}
	return &window
}

type resetCreditPayload struct {
	ID          string  `json:"id"`
	ResetType   string  `json:"reset_type"`
	Status      string  `json:"status"`
	GrantedAt   *string `json:"granted_at"`
	ExpiresAt   *string `json:"expires_at"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	RedeemedAt  *string `json:"redeemed_at"`
}

type consumePayload struct {
	Code         string             `json:"code"`
	WindowsReset int                `json:"windows_reset"`
	Credit       resetCreditPayload `json:"credit"`
}

func (p resetCreditPayload) value() application.ResetCredit {
	return application.ResetCredit{
		ID: p.ID, ResetType: p.ResetType, Status: p.Status,
		GrantedAt: parseResetCreditTime(p.GrantedAt), ExpiresAt: parseResetCreditTime(p.ExpiresAt),
		RedeemedAt: parseResetCreditTime(p.RedeemedAt), Title: p.Title, Description: p.Description,
	}
}

func parseResetCreditTime(value *string) *time.Time {
	if value == nil || *value == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, strings.ReplaceAll(*value, "Z", "+00:00"))
	if err != nil {
		return nil
	}
	return &parsed
}
