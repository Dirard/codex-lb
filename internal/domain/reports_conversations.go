package domain

import "time"

type ConversationEntry struct {
	ConversationID        string    `json:"conversationId"`
	FirstRequest          time.Time `json:"firstRequest"`
	LastRequest           time.Time `json:"lastRequest"`
	RequestCount          int64     `json:"requestCount"`
	RepresentativeAccount *string   `json:"representativeAccount"`
	RemainingAccountCount int64     `json:"remainingAccountCount"`
	APIKeyID              *string   `json:"apiKeyId"`
	APIKeyName            *string   `json:"apiKeyName"`
	RepresentativeModel   *string   `json:"representativeModel"`
	RemainingModelCount   int64     `json:"remainingModelCount"`
	TotalTokens           int64     `json:"totalTokens"`
	CachedInputTokens     *int64    `json:"cachedInputTokens"`
	TotalCostUSD          float64   `json:"totalCostUsd"`
}

type ConversationsResponse struct {
	Conversations []ConversationEntry `json:"conversations"`
	Total         int64               `json:"total"`
	HasMore       bool                `json:"hasMore"`
}

type ConversationModelStat struct {
	ModelEffort struct {
		Model           string  `json:"model"`
		ReasoningEffort *string `json:"reasoningEffort"`
	} `json:"modelEffort"`
	Requests          int64   `json:"reqs"`
	TotalElapsedTime  int64   `json:"totalElapsedTime"`
	TotalInputTokens  int64   `json:"totalInputTokens"`
	CachedInputTokens *int64  `json:"cachedInputTokens"`
	TotalOutputTokens int64   `json:"totalOutputTokens"`
	TotalCostUSD      float64 `json:"totalCostUsd"`
}

type ConversationDetails struct {
	ConversationID         string                  `json:"conversationId"`
	Start                  time.Time               `json:"start"`
	Latest                 time.Time               `json:"latest"`
	AccountCount           int64                   `json:"accountCount"`
	TotalElapsedTime       int64                   `json:"totalElapsedTime"`
	DominantUserAgentGroup *string                 `json:"dominantUseragentGroup"`
	ModelStats             []ConversationModelStat `json:"modelStats"`
}

type ConversationFilter struct {
	Since         time.Time
	Search        string
	Limit, Offset int
}
