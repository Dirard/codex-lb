package chatgpt

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"codex-lb/internal/domain"
)

const (
	defaultCatalogBaseURL = "https://chatgpt.com/backend-api/codex"
	catalogResponseLimit  = 8 << 20
	catalogFetchTimeout   = 15 * time.Second
)

type ModelCatalogClient struct {
	// ResolveClientVersion is configured at startup and read for every fetch.
	ResolveClientVersion func(context.Context) (string, error)
	baseURL              string
	clientVersion        string
	client               *http.Client
}

func NewModelCatalogClient(baseURL, clientVersion string, client *http.Client) *ModelCatalogClient {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultCatalogBaseURL
	}
	if strings.TrimSpace(clientVersion) == "" {
		clientVersion = domain.DefaultCodexClientVersion
	}
	if client == nil {
		client = &http.Client{}
	}
	bounded := *client
	bounded.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	client = &bounded
	return &ModelCatalogClient{baseURL: strings.TrimRight(baseURL, "/"), clientVersion: clientVersion, client: client}
}

func (c *ModelCatalogClient) Fetch(ctx context.Context, account domain.Account, accessToken string) ([]domain.CatalogModel, error) {
	ctx, cancel := context.WithTimeout(ctx, catalogFetchTimeout)
	defer cancel()
	version := c.clientVersion
	if c.ResolveClientVersion != nil {
		var err error
		version, err = c.ResolveClientVersion(ctx)
		if err != nil {
			return nil, &domain.CatalogFetchError{Err: err}
		}
	}
	if !domain.ValidCodexClientVersion(version) {
		return nil, &domain.CatalogFetchError{Err: domain.ErrInvalid}
	}
	requestURL := c.baseURL + "/models?client_version=" + url.QueryEscape(version)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, &domain.CatalogFetchError{Status: 0, Err: err}
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	if account.ChatGPTAccountID != "" {
		req.Header.Set("ChatGPT-Account-ID", account.ChatGPTAccountID)
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Originator", "codex_cli_rs")
	req.Header.Set("User-Agent", "codex_cli_rs/"+version)
	response, err := c.client.Do(req)
	if err != nil {
		return nil, &domain.CatalogFetchError{Status: 0, Err: err}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, catalogResponseLimit+1))
	if err != nil || len(body) > catalogResponseLimit {
		return nil, &domain.CatalogFetchError{Status: response.StatusCode, Err: fmt.Errorf("invalid catalog response length")}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &domain.CatalogFetchError{Status: response.StatusCode, Err: fmt.Errorf("catalog endpoint returned HTTP %d", response.StatusCode)}
	}
	var payload struct {
		Models []map[string]json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.Models == nil {
		return nil, &domain.CatalogFetchError{Status: http.StatusBadGateway, Err: fmt.Errorf("catalog response missing models")}
	}
	models := make([]domain.CatalogModel, 0, len(payload.Models))
	for _, raw := range payload.Models {
		model, err := domain.ParseCatalogModel(raw)
		if err != nil {
			continue
		}
		models = append(models, model)
	}
	return models, nil
}
