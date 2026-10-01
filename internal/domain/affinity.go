package domain

import "time"

type AffinityKind string

const (
	AffinityPromptCache  AffinityKind = "prompt_cache"
	AffinityStickyThread AffinityKind = "sticky_thread"
	AffinityCodexSession AffinityKind = "codex_session"
)

func (kind AffinityKind) Valid() bool {
	return kind == AffinityPromptCache || kind == AffinityStickyThread || kind == AffinityCodexSession
}

// AffinityBinding is a key-scoped locality preference, never proof of ownership.
type AffinityBinding struct {
	Key       string
	Kind      AffinityKind
	APIKeyID  string
	AccountID string
	CreatedAt time.Time
	UpdatedAt time.Time
	Version   int64
}

type AffinityIdentifier struct {
	Key  string       `json:"key"`
	Kind AffinityKind `json:"kind"`
}

type AffinityEntry struct {
	AffinityIdentifier
	AccountID   string     `json:"accountId"`
	DisplayName string     `json:"displayName"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
	ExpiresAt   *time.Time `json:"expiresAt"`
	IsStale     bool       `json:"isStale"`
}

type AffinityFilter struct {
	Kind                   AffinityKind
	StaleOnly              bool
	AccountQuery, KeyQuery string
	SortBy, SortDir        string
	Limit, Offset          int
}

type AffinityList struct {
	Entries               []AffinityEntry `json:"entries"`
	StalePromptCacheCount int             `json:"stalePromptCacheCount"`
	Total                 int             `json:"total"`
	HasMore               bool            `json:"hasMore"`
}

type AffinityDeleteFailure struct {
	AffinityIdentifier
	Reason string `json:"reason"`
}

type AffinityDeleteResult struct {
	DeletedCount int                     `json:"deletedCount"`
	Deleted      []AffinityIdentifier    `json:"deleted"`
	Failed       []AffinityDeleteFailure `json:"failed"`
}
