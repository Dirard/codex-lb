package application

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"codex-lb/internal/domain"
)

const maxDiagnosticBody = 128 << 10
const maxDiagnosticEventBytes = 64 << 10

type ErrorArchiveRepository interface {
	SaveErrorArchive(context.Context, domain.ErrorArchive, int64) error
	QueryErrorArchives(context.Context, domain.ErrorArchiveFilter, time.Time) (domain.ErrorArchivePage, error)
	PruneErrorArchives(context.Context, time.Time) error
	ListErrorArchiveDays(context.Context, time.Time) ([]domain.ErrorArchiveDay, error)
}

type DiagnosticRecorder func(context.Context, ErrorDiagnostic)

type ErrorDiagnostic struct {
	RequestID         string
	AccountID         string
	AccountGeneration int64
	KeyID             string
	Transport         string
	Model             string
	Status            string
	ErrorCode         string
	OccurredAt        time.Time
	Request, Response json.RawMessage
	Events            []json.RawMessage
	EventsTruncated   bool
}

type ErrorArchiveContent struct {
	Model           string            `json:"model"`
	ErrorCode       string            `json:"errorCode"`
	Request         json.RawMessage   `json:"request"`
	Response        json.RawMessage   `json:"response"`
	Events          []json.RawMessage `json:"events"`
	EventsTruncated bool              `json:"eventsTruncated"`
}

type ErrorArchives struct {
	repo   ErrorArchiveRepository
	cipher SecretCipher
}

func NewErrorArchives(repo ErrorArchiveRepository, cipher SecretCipher) *ErrorArchives {
	return &ErrorArchives{repo: repo, cipher: cipher}
}

// RecordFailure is called after accounting settles. Success and cancellation
// never create diagnostic content records, even if a caller supplies a payload.
func (s *ErrorArchives) RecordFailure(ctx context.Context, d ErrorDiagnostic) error {
	if d.Status != "error" && d.Status != "failed" {
		return nil
	}
	if d.RequestID == "" || len(d.RequestID) > 256 || len(d.Model) > 256 || d.OccurredAt.IsZero() {
		return domain.ErrInvalid
	}
	content := ErrorArchiveContent{Model: d.Model, ErrorCode: safeDiagnosticCode(d.ErrorCode), Request: redactDiagnostic(d.Request), Response: redactDiagnostic(d.Response), Events: []json.RawMessage{}, EventsTruncated: d.EventsTruncated}
	bytes := 0
	for _, event := range d.Events {
		if len(content.Events) >= 64 || bytes+len(event) > maxDiagnosticEventBytes {
			content.EventsTruncated = true
			break
		}
		content.Events = append(content.Events, redactDiagnostic(event))
		bytes += len(event)
	}
	body, err := json.Marshal(content)
	if err != nil {
		return errors.New("could not encode protected error diagnostic")
	}
	encrypted, err := s.cipher.Encrypt(body)
	if err != nil {
		return err
	}
	return s.repo.SaveErrorArchive(ctx, domain.ErrorArchive{RequestID: d.RequestID, AccountID: d.AccountID, AccountGeneration: d.AccountGeneration, KeyID: d.KeyID, Transport: d.Transport, OccurredAt: d.OccurredAt, ExpiresAt: d.OccurredAt.Add(7 * 24 * time.Hour), ContentEncrypted: encrypted}, 64<<20)
}

func (s *ErrorArchives) Query(ctx context.Context, f domain.ErrorArchiveFilter) (domain.ErrorArchivePage, []ErrorArchiveContent, error) {
	if f.Limit == 0 {
		f.Limit = 100
	}
	if f.Limit < 1 || f.Limit > 200 || f.Offset < 0 || len(f.RequestID) > 256 || len(f.Transport) > 32 || f.RequestID == "" && f.Start == nil {
		return domain.ErrorArchivePage{}, nil, domain.ErrInvalid
	}
	page, err := s.repo.QueryErrorArchives(ctx, f, time.Now().UTC())
	if err != nil {
		return page, nil, err
	}
	contents := make([]ErrorArchiveContent, 0, len(page.Items))
	for _, item := range page.Items {
		plain, err := s.cipher.Decrypt(item.ContentEncrypted)
		if err != nil {
			return domain.ErrorArchivePage{}, nil, errors.New("could not decrypt protected error diagnostic")
		}
		var content ErrorArchiveContent
		if json.Unmarshal(plain, &content) != nil {
			return domain.ErrorArchivePage{}, nil, errors.New("invalid protected error diagnostic")
		}
		contents = append(contents, content)
	}
	return page, contents, nil
}

// DiagnosticEvents owns a bounded copy of event data until the attempt ends.
// On success it is discarded rather than sent to the diagnostic archive.
type DiagnosticEvents struct {
	Events    []json.RawMessage
	Truncated bool
	bytes     int
}

func (d *DiagnosticEvents) Add(event ResponseEvent) {
	if len(d.Events) >= 64 {
		d.Truncated = true
		return
	}
	data := event.Data
	if d.bytes+len(data) > maxDiagnosticEventBytes {
		d.Truncated = true
		kind := event.Type
		if len(kind) > 128 {
			kind = "unknown"
		}
		data, _ = json.Marshal(struct {
			Type    string `json:"type"`
			Omitted string `json:"omitted"`
			Bytes   int    `json:"bytes"`
		}{diagnosticTokens.ReplaceAllString(kind, "[redacted]"), "diagnostic size limit", len(event.Data)})
		if d.bytes+len(data) > maxDiagnosticEventBytes {
			return
		}
	}
	d.Events = append(d.Events, append(json.RawMessage(nil), data...))
	d.bytes += len(data)
}
