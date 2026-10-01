package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

func (s *Server) ConfigureErrorArchives(repo application.ErrorArchiveRepository) {
	s.archiveRepo = repo
	s.archives = application.NewErrorArchives(repo, s.cipher)
}

type archiveRecordView struct {
	FileName   *string                         `json:"fileName"`
	Timestamp  *time.Time                      `json:"timestamp"`
	RequestID  *string                         `json:"requestId"`
	Direction  *string                         `json:"direction"`
	Kind       *string                         `json:"kind"`
	Transport  *string                         `json:"transport"`
	AccountID  *string                         `json:"accountId"`
	Method     *string                         `json:"method"`
	URL        *string                         `json:"url"`
	StatusCode *int                            `json:"statusCode"`
	Headers    map[string]string               `json:"headers"`
	Payload    application.ErrorArchiveContent `json:"payload"`
	Extra      map[string]any                  `json:"extra"`
}

func (s *Server) registerErrorArchiveRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/conversation-archive/files", func(w http.ResponseWriter, r *http.Request) {
		days, err := s.archiveRepo.ListErrorArchiveDays(r.Context(), time.Now().UTC())
		if err != nil {
			s.fail(w, err)
			return
		}
		type fileView struct {
			Name       string    `json:"name"`
			Date       string    `json:"date"`
			SizeBytes  int64     `json:"sizeBytes"`
			Compressed bool      `json:"compressed"`
			ModifiedAt time.Time `json:"modifiedAt"`
		}
		files := make([]fileView, 0, len(days))
		for _, day := range days {
			files = append(files, fileView{Name: "errors-" + day.Day, Date: day.Day, SizeBytes: day.Bytes, ModifiedAt: day.ModifiedAt})
		}
		writeJSON(w, 200, files)
	})
	mux.HandleFunc("GET /api/conversation-archive/records", s.errorArchiveRecords)
}

func (s *Server) errorArchiveRecords(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := domain.ErrorArchiveFilter{RequestID: q.Get("requestId"), Transport: q.Get("transport"), Limit: 100}
	for _, field := range []struct {
		name  string
		value *int
	}{{"limit", &filter.Limit}, {"offset", &filter.Offset}} {
		if q.Has(field.name) {
			n, err := strconv.Atoi(q.Get(field.name))
			if err != nil {
				writeError(w, 400, "invalid_request", "Invalid archive pagination")
				return
			}
			*field.value = n
		}
	}
	if filename := q.Get("file"); filename != "" {
		day, err := time.Parse("2006-01-02", strings.TrimPrefix(filename, "errors-"))
		if err != nil || filename != "errors-"+day.Format("2006-01-02") {
			writeError(w, 400, "invalid_archive_file", "Invalid archive day")
			return
		}
		end := day.AddDate(0, 0, 1)
		filter.Start, filter.End = &day, &end
	}
	if raw := q.Get("requestedAt"); raw != "" {
		if _, err := time.Parse(time.RFC3339Nano, raw); err != nil {
			writeError(w, 400, "invalid_request", "Invalid archive timestamp")
			return
		}
	}
	page, contents, err := s.archives.Query(r.Context(), filter)
	if err != nil {
		s.fail(w, err)
		return
	}
	records := []archiveRecordView{}
	if q.Get("direction") != "" && q.Get("direction") != "upstream" || q.Get("kind") != "" && q.Get("kind") != "error" {
		page.Total = 0
	} else {
		for i, item := range page.Items {
			file, direction, kind := "errors-"+item.OccurredAt.UTC().Format("2006-01-02"), "upstream", "error"
			records = append(records, archiveRecordView{FileName: &file, Timestamp: &item.OccurredAt, RequestID: &item.RequestID, Direction: &direction, Kind: &kind, Transport: &item.Transport, AccountID: &item.AccountID, Payload: contents[i]})
		}
	}
	writeJSON(w, 200, struct {
		Records []archiveRecordView `json:"records"`
		Total   int                 `json:"total"`
		HasMore bool                `json:"hasMore"`
	}{records, page.Total, filter.Offset+len(records) < page.Total})
}
