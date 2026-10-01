package chatgpt

import (
	"context"
	"net/url"

	"codex-lb/internal/application"
)

func (c *Client) Refresh(ctx context.Context, refreshToken string) (application.OAuthTokens, error) {
	if refreshToken == "" {
		return application.OAuthTokens{}, oauthError("invalid_refresh_token", "Refresh token is missing", 0)
	}
	form := url.Values{"grant_type": {"refresh_token"}, "client_id": {c.config.ClientID}, "refresh_token": {refreshToken}}
	var payload tokenPayload
	if err := c.postForm(ctx, "/oauth/token", form, &payload); err != nil {
		return application.OAuthTokens{}, err
	}
	if code := payload.errorCode(); code != "" {
		return application.OAuthTokens{}, providerOAuthError(200, code)
	}
	if payload.AccessToken == "" {
		return application.OAuthTokens{}, oauthError("invalid_response", "OAuth refresh omitted access token", 0)
	}
	return application.OAuthTokens{AccessToken: payload.AccessToken, RefreshToken: payload.RefreshToken, IDToken: payload.IDToken}, nil
}
