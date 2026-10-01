package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"codex-lb/internal/domain"
)

// registerAffinityRoutes manages locality hints only. The enclosing admin mux
// supplies authentication and CSRF protection; no correctness aliases are exposed.
func (s *Server) registerAffinityRoutes(mux *http.ServeMux) {
	for _, path := range []string{"/api/sticky-sessions", "/api/sticky-sessions/{$}"} {
		mux.HandleFunc("GET "+path, s.listAffinities)
	}
	for _, path := range []string{"/api/sticky-sessions/delete", "/api/sticky-sessions/delete/{$}"} {
		mux.HandleFunc("POST "+path, func(w http.ResponseWriter, r *http.Request) {
			var payload struct {
				Sessions []domain.AffinityIdentifier `json:"sessions"`
			}
			if !decodeJSON(w, r, &payload) {
				return
			}
			result, err := s.store.DeleteAffinities(r.Context(), payload.Sessions)
			if err != nil {
				s.fail(w, err)
				return
			}
			writeJSON(w, http.StatusOK, result)
		})
	}
	for _, path := range []string{"/api/sticky-sessions/delete-filtered", "/api/sticky-sessions/delete-filtered/{$}"} {
		mux.HandleFunc("POST "+path, func(w http.ResponseWriter, r *http.Request) {
			var payload struct {
				StaleOnly    bool   `json:"staleOnly"`
				AccountQuery string `json:"accountQuery"`
				KeyQuery     string `json:"keyQuery"`
			}
			if !decodeJSON(w, r, &payload) {
				return
			}
			s.deleteFilteredAffinities(w, r, domain.AffinityFilter{StaleOnly: payload.StaleOnly, AccountQuery: payload.AccountQuery, KeyQuery: payload.KeyQuery})
		})
	}
	for _, path := range []string{"/api/sticky-sessions/purge", "/api/sticky-sessions/purge/{$}"} {
		mux.HandleFunc("POST "+path, func(w http.ResponseWriter, r *http.Request) {
			var payload struct {
				StaleOnly *bool `json:"staleOnly"`
			}
			if r.ContentLength != 0 && !decodeJSON(w, r, &payload) {
				return
			}
			if payload.StaleOnly != nil && !*payload.StaleOnly {
				s.fail(w, domain.ErrInvalid)
				return
			}
			s.deleteFilteredAffinities(w, r, domain.AffinityFilter{StaleOnly: true})
		})
	}
	mux.HandleFunc("DELETE /api/sticky-sessions/{kind}/{key...}", func(w http.ResponseWriter, r *http.Request) {
		result, err := s.store.DeleteAffinities(r.Context(), []domain.AffinityIdentifier{{Key: r.PathValue("key"), Kind: domain.AffinityKind(r.PathValue("kind"))}})
		if err != nil {
			s.fail(w, err)
			return
		}
		if result.DeletedCount == 0 {
			writeError(w, http.StatusNotFound, "sticky_session_not_found", "Sticky session not found")
			return
		}
		writeJSON(w, http.StatusOK, struct {
			Status string `json:"status"`
		}{"deleted"})
	})
}

func (s *Server) listAffinities(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	filter := domain.AffinityFilter{Kind: domain.AffinityKind(query.Get("kind")), AccountQuery: query.Get("accountQuery"),
		KeyQuery: query.Get("keyQuery"), SortBy: query.Get("sortBy"), SortDir: query.Get("sortDir"), Limit: 100}
	for _, field := range []struct {
		name string
		dst  *int
	}{{"limit", &filter.Limit}, {"offset", &filter.Offset}} {
		if raw := query.Get(field.name); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil {
				s.fail(w, domain.ErrInvalid)
				return
			}
			*field.dst = value
		}
	}
	if raw := query.Get("staleOnly"); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			s.fail(w, domain.ErrInvalid)
			return
		}
		filter.StaleOnly = value
	}
	settings, err := s.store.LoadSettings(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	page, err := s.store.ListAffinities(r.Context(), filter, time.Now().UTC(), time.Duration(settings.OpenAICacheAffinityMaxAgeSeconds)*time.Second)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) deleteFilteredAffinities(w http.ResponseWriter, r *http.Request, filter domain.AffinityFilter) {
	settings, err := s.store.LoadSettings(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	count, err := s.store.DeleteFilteredAffinities(r.Context(), filter, time.Now().UTC(), time.Duration(settings.OpenAICacheAffinityMaxAgeSeconds)*time.Second)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		DeletedCount int `json:"deletedCount"`
	}{count})
}
