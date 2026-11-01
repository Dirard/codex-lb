package httpapi

import (
	"context"
	"encoding/base64"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

// VideoHandler serves the separate REST surface of video generation. It shares
// only the API key and the request-body budget with the proxy routes.
type VideoHandler struct {
	store interface {
		FindAPIKeyByHash(context.Context, string) (domain.APIKey, error)
	}
	service *application.VideoService
	bodies  *BodyBudget
}

func NewVideoHandler(store interface {
	FindAPIKeyByHash(context.Context, string) (domain.APIKey, error)
}, service *application.VideoService) *VideoHandler {
	return &VideoHandler{store: store, service: service, bodies: NewBodyBudget(0, 0)}
}

// ConfigureBodyBudget charges bodies to the budget shared by every proxy route.
func (h *VideoHandler) ConfigureBodyBudget(bodies *BodyBudget) { h.bodies = bodies }

func (h *VideoHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /video/v1/generations", h.create)
	mux.HandleFunc("GET /video/v1/generations", h.list)
	mux.HandleFunc("GET /video/v1/generations/{id}", h.status)
}

// authorize requires a real API key on the video allow list. Keyless local
// access has no bearer key, so it never reaches this function's success path.
func (h *VideoHandler) authorize(w http.ResponseWriter, r *http.Request) (string, bool) {
	key, err := authenticateBearerKey(r, h.store)
	if err == nil {
		err = h.service.KeyAllowed(r.Context(), key.ID)
	}
	if err != nil {
		writeVideoError(w, err)
		return "", false
	}
	return key.ID, true
}

func (h *VideoHandler) create(w http.ResponseWriter, r *http.Request) {
	keyID, ok := h.authorize(w, r)
	if !ok {
		return
	}
	lease := h.bodies.lease(keyID)
	defer lease.release()
	body, err := readOperationJSON(w, r, lease, domain.MaxRequestBodyBytes)
	if err != nil {
		writeVideoError(w, err)
		return
	}
	job, err := h.service.Generate(r.Context(), keyID, body)
	if err != nil {
		writeVideoError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (h *VideoHandler) status(w http.ResponseWriter, r *http.Request) {
	keyID, ok := h.authorize(w, r)
	if !ok {
		return
	}
	job, err := h.service.Status(r.Context(), keyID, r.PathValue("id"))
	if err != nil {
		writeVideoError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (h *VideoHandler) list(w http.ResponseWriter, r *http.Request) {
	keyID, ok := h.authorize(w, r)
	if !ok {
		return
	}
	before, limit, ok := videoPageParams(w, r)
	if !ok {
		return
	}
	page, err := h.service.List(r.Context(), keyID, before, limit)
	if err != nil {
		writeVideoError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": page.Jobs, "next_cursor": encodeVideoCursor(page.Next)})
}

func writeVideoError(w http.ResponseWriter, err error) {
	var exhausted *application.VideoPoolExhausted
	if errors.As(err, &exhausted) {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(max(exhausted.RetryAfter, time.Second).Seconds()))))
		writeError(w, http.StatusTooManyRequests, "video_pool_exhausted", exhausted.Error())
		return
	}
	writeProxyError(w, err)
}

func videoPageParams(w http.ResponseWriter, r *http.Request) (*domain.VideoJobCursor, int, bool) {
	query := r.URL.Query()
	limit := 50
	if value := query.Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 100 {
			writeProxyError(w, &application.ProxyError{Code: "invalid_request", Status: 400, Message: "limit must be from 1 to 100", Param: "limit"})
			return nil, 0, false
		}
		limit = parsed
	}
	cursor := query.Get("cursor")
	if cursor == "" {
		return nil, limit, true
	}
	before, ok := decodeVideoCursor(cursor)
	if !ok {
		writeProxyError(w, &application.ProxyError{Code: "invalid_request", Status: 400, Message: "Invalid cursor", Param: "cursor"})
		return nil, 0, false
	}
	return before, limit, true
}

func encodeVideoCursor(cursor *domain.VideoJobCursor) *string {
	if cursor == nil {
		return nil
	}
	encoded := base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(cursor.CreatedAt.UnixMilli(), 10) + ":" + cursor.ID))
	return &encoded
}

func decodeVideoCursor(value string) (*domain.VideoJobCursor, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, false
	}
	at, id, found := strings.Cut(string(raw), ":")
	created, parseErr := strconv.ParseInt(at, 10, 64)
	if !found || parseErr != nil || id == "" {
		return nil, false
	}
	return &domain.VideoJobCursor{CreatedAt: time.UnixMilli(created).UTC(), ID: id}, true
}

// registerVideoAdminRoutes serves the "Video generation" dashboard page.
func (s *Server) registerVideoAdminRoutes(mux *http.ServeMux) {
	if s.video == nil {
		return
	}
	mux.HandleFunc("GET /api/video/accounts", s.listVideoAccounts)
	mux.HandleFunc("POST /api/video/accounts/login", s.startVideoLogin)
	mux.HandleFunc("GET /api/video/accounts/login/{id}", s.videoLoginStatus)
	mux.HandleFunc("DELETE /api/video/accounts/login/{id}", s.cancelVideoLogin)
	mux.HandleFunc("PATCH /api/video/accounts/{id}", s.updateVideoAccount)
	mux.HandleFunc("DELETE /api/video/accounts/{id}", s.deleteVideoAccount)
	mux.HandleFunc("GET /api/video/keys", s.listVideoKeys)
	mux.HandleFunc("PUT /api/video/keys/{id}", s.allowVideoKey)
	mux.HandleFunc("DELETE /api/video/keys/{id}", s.denyVideoKey)
	mux.HandleFunc("GET /api/video/jobs", s.listVideoJobEntries)
	mux.HandleFunc("GET /api/video/summary", s.videoSummary)
}

func (s *Server) videoAdminFail(w http.ResponseWriter, err error) {
	var request *application.ProxyError
	switch {
	case errors.As(err, &request):
		writeProxyError(w, err)
	case errors.Is(err, domain.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Not found")
	default:
		s.fail(w, err)
	}
}

func (s *Server) listVideoAccounts(w http.ResponseWriter, r *http.Request) {
	accounts, err := s.video.ListAccounts(r.Context())
	if err != nil {
		s.videoAdminFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": accounts})
}

func (s *Server) startVideoLogin(w http.ResponseWriter, r *http.Request) {
	login, err := s.video.StartLogin(r.Context())
	if err != nil {
		s.videoAdminFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, login)
}

func (s *Server) videoLoginStatus(w http.ResponseWriter, r *http.Request) {
	status, err := s.video.LoginStatus(r.Context(), r.PathValue("id"))
	if err != nil {
		s.videoAdminFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) cancelVideoLogin(w http.ResponseWriter, r *http.Request) {
	s.video.CancelLogin(r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) updateVideoAccount(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Label  *string                    `json:"label"`
		Status *domain.VideoAccountStatus `json:"status"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	if err := s.video.UpdateAccount(r.Context(), r.PathValue("id"), request.Label, request.Status); err != nil {
		s.videoAdminFail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteVideoAccount(w http.ResponseWriter, r *http.Request) {
	if err := s.video.DeleteAccount(r.Context(), r.PathValue("id")); err != nil {
		s.videoAdminFail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listVideoKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := s.video.ListKeys(r.Context())
	if err != nil {
		s.videoAdminFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": keys})
}

func (s *Server) allowVideoKey(w http.ResponseWriter, r *http.Request) {
	if err := s.video.AllowKey(r.Context(), r.PathValue("id")); err != nil {
		s.videoAdminFail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) denyVideoKey(w http.ResponseWriter, r *http.Request) {
	if err := s.video.DenyKey(r.Context(), r.PathValue("id")); err != nil {
		s.videoAdminFail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type videoJobEntryView struct {
	ID               string   `json:"id"`
	KeyName          string   `json:"keyName"`
	AccountLabel     string   `json:"accountLabel"`
	Model            string   `json:"model"`
	Mode             string   `json:"mode"`
	RequestedSeconds int      `json:"requestedSeconds"`
	Status           string   `json:"status"`
	DurationSeconds  *float64 `json:"durationSeconds"`
	CostUSD          *float64 `json:"costUsd"`
	ErrorCode        string   `json:"errorCode"`
	ErrorMessage     string   `json:"errorMessage"`
	// The last answer of xAI about this job, for debugging. Status 0 with a body
	// is a read that got no HTTP answer.
	UpstreamHTTPStatus int        `json:"upstreamHttpStatus"`
	UpstreamBody       string     `json:"upstreamBody"`
	UpstreamAt         *time.Time `json:"upstreamAt"`
	CreatedAt          time.Time  `json:"createdAt"`
	FinishedAt         *time.Time `json:"finishedAt"`
}

func (s *Server) listVideoJobEntries(w http.ResponseWriter, r *http.Request) {
	before, limit, ok := videoPageParams(w, r)
	if !ok {
		return
	}
	entries, next, err := s.video.Jobs(r.Context(), before, limit)
	if err != nil {
		s.videoAdminFail(w, err)
		return
	}
	views := make([]videoJobEntryView, 0, len(entries))
	for _, e := range entries {
		view := videoJobEntryView{ID: e.ID, KeyName: e.KeyName, AccountLabel: e.AccountLabel, Model: e.Model, Mode: e.Mode, RequestedSeconds: e.RequestedSeconds,
			Status: string(e.Status), DurationSeconds: e.DurationSeconds, ErrorCode: e.ErrorCode, ErrorMessage: e.ErrorMessage, CreatedAt: e.CreatedAt, FinishedAt: e.FinishedAt}
		view.UpstreamHTTPStatus, view.UpstreamBody = e.Answer.HTTPStatus, e.Answer.Body
		if !e.Answer.At.IsZero() {
			view.UpstreamAt = &e.Answer.At
		}
		if e.CostMicrodollars != nil {
			cost := float64(*e.CostMicrodollars) / 1e6
			view.CostUSD = &cost
		}
		views = append(views, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": views, "nextCursor": encodeVideoCursor(next)})
}

type videoTotalsView struct {
	Jobs    int64   `json:"jobs"`
	Done    int64   `json:"done"`
	Seconds float64 `json:"seconds"`
	CostUSD float64 `json:"costUsd"`
}

func viewVideoTotals(totals domain.VideoPeriodTotals) videoTotalsView {
	return videoTotalsView{Jobs: totals.Jobs, Done: totals.Done, Seconds: totals.Seconds, CostUSD: float64(totals.CostMicrodollars) / 1e6}
}

func (s *Server) videoSummary(w http.ResponseWriter, r *http.Request) {
	summary, err := s.video.Summary(r.Context())
	if err != nil {
		s.videoAdminFail(w, err)
		return
	}
	var response = struct {
		Today videoTotalsView `json:"today"`
		Week  videoTotalsView `json:"week"`
		Month videoTotalsView `json:"month"`
	}{viewVideoTotals(summary.Today), viewVideoTotals(summary.Week), viewVideoTotals(summary.Month)}
	writeJSON(w, http.StatusOK, response)
}
