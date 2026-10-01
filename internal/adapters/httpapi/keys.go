package httpapi

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"codex-lb/internal/domain"
)

type keyView struct {
	domain.APIKey
	UsageSummary usageSummary `json:"usageSummary"`
}

func (s *Server) listKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := s.store.ListAPIKeys(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	views := make([]keyView, 0, len(keys))
	for _, key := range keys {
		totals, err := s.store.UsageTotals(r.Context(), key.ID, "")
		if err != nil {
			s.fail(w, err)
			return
		}
		views = append(views, keyView{key, summarizeUsage(totals)})
	}
	writeJSON(w, http.StatusOK, views)
}

func issueKey() (plain, hash, prefix string) {
	plain = newID("sk-clb-") + newID("")
	hash = fmt.Sprintf("%x", sha256.Sum256([]byte(plain)))
	return plain, hash, plain[:15]
}

func (s *Server) saveKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var key domain.APIKey
	var err error
	plain := ""
	if id != "" {
		key, err = s.store.GetAPIKey(r.Context(), id)
		if err != nil {
			s.fail(w, err)
			return
		}
	} else {
		key = domain.APIKey{ID: newID("key_"), IsActive: true, CreatedAt: time.Now().UTC(), TrafficClass: "foreground", UsageSections: "upstream_limits,account_pool_usage"}
		plain, key.KeyHash, key.KeyPrefix = issueKey()
	}
	payload := struct {
		domain.APIKey
		WeeklyTokenLimit json.RawMessage `json:"weeklyTokenLimit"`
		ResetUsage       bool            `json:"resetUsage"`
		AssignedAccounts *[]string       `json:"assignedAccountIds"`
		AssignedSources  *[]string       `json:"assignedSourceIds"`
	}{APIKey: key}
	if !decodeJSON(w, r, &payload) {
		return
	}
	// Server-owned fields cannot be patched through the dashboard wire format.
	payload.ID, payload.KeyHash, payload.KeyPrefix = key.ID, key.KeyHash, key.KeyPrefix
	payload.CreatedAt, payload.LastUsedAt = key.CreatedAt, key.LastUsedAt
	if payload.WeeklyTokenLimit != nil {
		filtered := make([]domain.LimitRule, 0, len(payload.Limits)+1)
		for _, limit := range payload.Limits {
			if limit.Type != domain.LimitTotalTokens || limit.Window != domain.WindowWeekly || limit.ModelFilter != nil {
				filtered = append(filtered, limit)
			}
		}
		if string(payload.WeeklyTokenLimit) != "null" {
			var amount int64
			if json.Unmarshal(payload.WeeklyTokenLimit, &amount) != nil || amount <= 0 {
				writeError(w, http.StatusUnprocessableEntity, "invalid_limit", "Weekly token limit must be a positive integer")
				return
			}
			filtered = append(filtered, domain.LimitRule{Type: domain.LimitTotalTokens, Window: domain.WindowWeekly, MaxValue: amount})
		}
		payload.Limits = filtered
	}
	if payload.AssignedAccounts != nil {
		payload.AssignedAccountIDs = *payload.AssignedAccounts
		payload.AccountAssignmentScopeEnabled = len(payload.AssignedAccountIDs) != 0
	}
	if payload.AssignedSources != nil {
		payload.AssignedSourceIDs = *payload.AssignedSources
		payload.SourceAssignmentScopeEnabled = len(payload.AssignedSourceIDs) != 0
	}
	if payload.GroupID != nil {
		payload.AccountAssignmentScopeEnabled = true
	}
	if err := s.store.SaveAPIKey(r.Context(), payload.APIKey, time.Now().UTC()); err != nil {
		s.fail(w, err)
		return
	}
	if payload.ResetUsage {
		if err := s.store.ResetAPIKeyUsage(r.Context(), key.ID, time.Now().UTC()); err != nil {
			s.fail(w, err)
			return
		}
	}
	key, err = s.store.GetAPIKey(r.Context(), key.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	if plain == "" {
		writeJSON(w, http.StatusOK, key)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		domain.APIKey
		Key string `json:"key"`
	}{key, plain})
}

func (s *Server) deleteKey(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteAPIKey(r.Context(), r.PathValue("id")); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) regenerateKey(w http.ResponseWriter, r *http.Request) {
	key, err := s.store.GetAPIKey(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	plain, hash, prefix := issueKey()
	key.KeyHash, key.KeyPrefix = hash, prefix
	if err := s.store.SaveAPIKey(r.Context(), key, time.Now().UTC()); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		domain.APIKey
		Key string `json:"key"`
	}{key, plain})
}
