package domain

import "time"

type APIKeyTrendBucket struct {
	At      time.Time
	Tokens  int64
	CostUSD float64
}

type APIKeyTrendsResponse struct {
	KeyID  string       `json:"keyId"`
	Cost   []TrendPoint `json:"cost"`
	Tokens []TrendPoint `json:"tokens"`
}

type APIKeyAccountCost struct {
	AccountID *string `json:"accountId"`
	Email     *string `json:"email"`
	CostUSD   float64 `json:"costUsd"`
	IsDeleted bool    `json:"isDeleted"`
}

type APIKeyUsage7DayResponse struct {
	KeyID             string              `json:"keyId"`
	TotalTokens       int64               `json:"totalTokens"`
	TotalCostUSD      float64             `json:"totalCostUsd"`
	TotalRequests     int64               `json:"totalRequests"`
	CachedInputTokens int64               `json:"cachedInputTokens"`
	AccountCosts      []APIKeyAccountCost `json:"accountCosts"`
}
