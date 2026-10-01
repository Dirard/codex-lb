package httpapi

import (
	"net/http"

	"codex-lb/internal/domain"
)

func (s *Server) setAccountGroups(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		GroupIDs *[]string `json:"groupIds"`
	}
	if !decodeJSON(w, r, &payload) {
		return
	}
	if payload.GroupIDs == nil {
		s.fail(w, domain.ErrInvalid)
		return
	}
	accountID := r.PathValue("id")
	if err := s.store.SetAccountGroups(r.Context(), accountID, *payload.GroupIDs); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		AccountID string   `json:"accountId"`
		GroupIDs  []string `json:"groupIds"`
	}{accountID, *payload.GroupIDs})
}
