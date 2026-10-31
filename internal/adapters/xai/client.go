// Package xai talks to the xAI video API and token endpoint for the video pool.
package xai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"codex-lb/internal/application"
)

const (
	DefaultAPIBaseURL  = "https://api.x.ai"
	DefaultAuthBaseURL = "https://auth.x.ai"
	maxResponseBytes   = 1 << 20
	callTimeout        = 60 * time.Second
	userAgent          = "codex-lb"
)

// Config sets the HTTP client and, for tests, the endpoints.
type Config struct {
	HTTPClient  *http.Client
	APIBaseURL  string
	AuthBaseURL string
}

type Client struct {
	http *http.Client
	api  string
	auth string
}

var _ application.VideoUpstream = (*Client)(nil)

func New(config Config) *Client {
	client := config.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	api, auth := config.APIBaseURL, config.AuthBaseURL
	if api == "" {
		api = DefaultAPIBaseURL
	}
	if auth == "" {
		auth = DefaultAuthBaseURL
	}
	return &Client{http: client, api: strings.TrimSuffix(api, "/"), auth: strings.TrimSuffix(auth, "/")}
}

// Refresh exchanges a refresh token. A definitive 4xx refusal is
// ErrVideoRefreshRefused; anything else leaves the account as it was.
func (c *Client) Refresh(ctx context.Context, clientID, refreshToken string) (application.VideoTokens, error) {
	status, body, err := c.form(ctx, "/oauth2/token", url.Values{"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {refreshToken}})
	if err != nil {
		return application.VideoTokens{}, err
	}
	if status >= 400 && status < 500 && status != http.StatusTooManyRequests {
		return application.VideoTokens{}, application.ErrVideoRefreshRefused
	}
	if status != http.StatusOK {
		return application.VideoTokens{}, fmt.Errorf("xai token endpoint status %d", status)
	}
	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if json.Unmarshal(body, &payload) != nil || payload.AccessToken == "" || payload.ExpiresIn <= 0 {
		return application.VideoTokens{}, errors.New("xai token endpoint answered without tokens")
	}
	if payload.RefreshToken == "" {
		payload.RefreshToken = refreshToken
	}
	return application.VideoTokens{Access: payload.AccessToken, Refresh: payload.RefreshToken, ExpiresIn: time.Duration(payload.ExpiresIn) * time.Second}, nil
}

// Revoke ends a refresh token's session.
func (c *Client) Revoke(ctx context.Context, clientID, refreshToken string) error {
	status, _, err := c.form(ctx, "/oauth2/revoke", url.Values{"client_id": {clientID}, "token": {refreshToken}, "token_type_hint": {"refresh_token"}})
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("xai revoke status %d", status)
	}
	return nil
}

// CreateVideo posts a validated body unchanged.
func (c *Client) CreateVideo(ctx context.Context, token string, body []byte) (application.VideoUpstreamResponse, error) {
	return c.video(ctx, http.MethodPost, "/v1/videos/generations", token, body)
}

// GetVideo reads one job.
func (c *Client) GetVideo(ctx context.Context, token, requestID string) (application.VideoUpstreamResponse, error) {
	return c.video(ctx, http.MethodGet, "/v1/videos/"+url.PathEscape(requestID), token, nil)
}

func (c *Client) video(ctx context.Context, method, path, token string, body []byte) (application.VideoUpstreamResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.api+path, reader)
	if err != nil {
		return application.VideoUpstreamResponse{}, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", userAgent)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return application.VideoUpstreamResponse{}, err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return application.VideoUpstreamResponse{}, err
	}
	if len(payload) > maxResponseBytes {
		return application.VideoUpstreamResponse{}, errors.New("xai response exceeds the bound")
	}
	return application.VideoUpstreamResponse{Status: response.StatusCode, Body: payload, RetryAfter: retryAfter(response.Header.Get("Retry-After"), time.Now())}, nil
}

func (c *Client) form(ctx context.Context, path string, values url.Values) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.auth+path, strings.NewReader(values.Encode()))
	if err != nil {
		return 0, nil, errors.New("invalid xai token request")
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", userAgent)
	response, err := c.http.Do(request)
	if err != nil {
		// A transport error can quote the request; the form holds a token.
		if ctx.Err() != nil {
			return 0, nil, ctx.Err()
		}
		return 0, nil, errors.New("xai token endpoint is unreachable")
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return 0, nil, errors.New("xai token response could not be read")
	}
	return response.StatusCode, payload, nil
}

func retryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil && at.After(now) {
		return at.Sub(now)
	}
	return 0
}
