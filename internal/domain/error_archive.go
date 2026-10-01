package domain

import "time"

// ErrorArchive contains only encrypted diagnostic content. It is independent
// of accounting rows and operational continuation state.
type ErrorArchive struct {
	RequestID         string
	AccountID         string
	AccountGeneration int64
	KeyID             string
	Transport         string
	OccurredAt        time.Time
	ExpiresAt         time.Time
	ContentEncrypted  []byte
}

type ErrorArchiveFilter struct {
	RequestID     string
	Start, End    *time.Time
	Transport     string
	Limit, Offset int
}

type ErrorArchivePage struct {
	Items []ErrorArchive
	Total int
}

type ErrorArchiveDay struct {
	Day        string
	Bytes      int64
	ModifiedAt time.Time
}
