package domain

import "time"

// Continuation pins a response to a key/provider/account. Replay bytes are
// encrypted operational state, never part of the all-request statistics archive.
type Continuation struct {
	ResponseID           string    `json:"responseId"`
	KeyID                string    `json:"keyId"`
	AccountID            string    `json:"accountId"`
	AccountGeneration    int64     `json:"-"`
	RouteRevision        int64     `json:"-"`
	ProviderID           string    `json:"providerId"`
	Model                string    `json:"model"`
	CreatedAt            time.Time `json:"createdAt"`
	ExpiresAt            time.Time `json:"expiresAt"`
	ContextEncrypted     []byte    `json:"-"`
	FilePinned           bool      `json:"filePinned"`
	QuotaRefused         bool      `json:"quotaRefused"`
	TurnStateForwardable bool      `json:"-"`
}

type ContinuationBounds struct {
	MaxRecords      int
	MaxContextBytes int64
}
