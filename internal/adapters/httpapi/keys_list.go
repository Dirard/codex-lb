package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

// keysFilterQuery lists the parameters of GET /api/keys/ids; GET /api/keys
// adds offset and limit. Any other parameter is refused so that a mistyped
// filter never widens a bulk selection.
var keysFilterQuery = map[string]bool{"q": true, "group": true, "status": true, "near_limit": true, "expiring_days": true,
	"inactive_days": true, "sort": true, "order": true, "timezone": true}

// registerKeysRoutes mounts the keys page reads and the bulk key actions
// inside the authenticated, compressed /api mux (dashboard redesign use cases
// 6 and 6.1).
func (s *Server) registerKeysRoutes(mux *http.ServeMux) {
	list := application.NewKeysListService(s.store, s.shellStatus(), nil)
	bulk := application.NewKeysBulkService(s.store, nil)
	for _, path := range []string{"/api/keys", "/api/keys/{$}"} {
		mux.Handle("GET "+path, serverTiming(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			params, ok := keysListParams(w, r, true)
			if !ok {
				return
			}
			page, err := list.List(r.Context(), params)
			if err != nil {
				s.failKeys(w, err)
				return
			}
			writeJSON(w, http.StatusOK, page)
		})))
	}
	for _, path := range []string{"/api/keys/ids", "/api/keys/ids/{$}"} {
		mux.Handle("GET "+path, serverTiming(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			params, ok := keysListParams(w, r, false)
			if !ok {
				return
			}
			ids, err := list.IDs(r.Context(), params)
			if err != nil {
				s.failKeys(w, err)
				return
			}
			writeJSON(w, http.StatusOK, ids)
		})))
	}
	for _, path := range []string{"/api/keys/bulk", "/api/keys/bulk/{$}"} {
		mux.HandleFunc("POST "+path, func(w http.ResponseWriter, r *http.Request) {
			request, ok := keysBulkRequest(w, r)
			if !ok {
				return
			}
			response, err := bulk.Apply(r.Context(), request)
			if err != nil {
				s.failKeys(w, err)
				return
			}
			failed := 0
			for _, result := range response.Results {
				if result.Code == domain.KeyBulkInternal {
					failed++
				}
			}
			if failed > 0 {
				s.logger.Warn("bulk key action failed for some keys", "action", string(request.Action), "failed", failed,
					"requested", len(request.IDs))
			}
			writeJSON(w, http.StatusOK, response)
		})
	}
}

// keysListParams validates the transport syntax of the keys list: known
// parameters at most once, whole numbers and booleans; an empty value is
// absent. Value ranges are checked by the use case.
func keysListParams(w http.ResponseWriter, r *http.Request, paged bool) (application.KeysListParams, bool) {
	var params application.KeysListParams
	query := r.URL.Query()
	refuse := func(message string) (application.KeysListParams, bool) {
		writeError(w, http.StatusBadRequest, "invalid_key_filters", message)
		return application.KeysListParams{}, false
	}
	for name, values := range query {
		known := keysFilterQuery[name] || (paged && (name == "offset" || name == "limit"))
		if !known || len(values) > 1 {
			return refuse("Unknown or repeated keys parameter")
		}
	}
	// number parses a whole number from least: offset starts at 0,
	// the day filters and the limit at 1.
	number := func(name string, least int, target *int) bool {
		value := query.Get(name)
		if value == "" {
			return true
		}
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < least || parsed > 1_000_000 {
			return false
		}
		*target = parsed
		return true
	}
	f := &params.Filter
	if !number("expiring_days", 1, &f.ExpiringDays) || !number("inactive_days", 1, &f.InactiveDays) ||
		!number("offset", 0, &params.Offset) || !number("limit", 1, &params.Limit) {
		return refuse("Day filters and limit must be whole numbers from 1, offset from 0")
	}
	switch query.Get("near_limit") {
	case "", "0", "false":
	case "1", "true":
		f.NearLimit = true
	default:
		return refuse("near_limit must be 1 or 0")
	}
	switch group := query.Get("group"); group {
	case "":
	case "ungrouped":
		f.Ungrouped = true
	default:
		f.GroupID = &group
	}
	f.Query, f.Status = query.Get("q"), query.Get("status")
	params.Sort, params.Order = domain.KeyListSort(query.Get("sort")), query.Get("order")
	loc, ok := requestTimezone(w, r)
	if !ok {
		return application.KeysListParams{}, false
	}
	params.Location = loc
	return params, true
}

// keysBulkRequest decodes POST /api/keys/bulk: groupId only and always with
// move_group (null is no group), limits only and always with set_limits.
func keysBulkRequest(w http.ResponseWriter, r *http.Request) (application.KeyBulkRequest, bool) {
	var body struct {
		IDs     []string            `json:"ids"`
		Action  string              `json:"action"`
		GroupID json.RawMessage     `json:"groupId"`
		Limits  *[]domain.LimitRule `json:"limits"`
	}
	if !decodeJSON(w, r, &body) {
		return application.KeyBulkRequest{}, false
	}
	refuse := func(message string) (application.KeyBulkRequest, bool) {
		writeError(w, http.StatusBadRequest, "invalid_bulk_request", message)
		return application.KeyBulkRequest{}, false
	}
	request := application.KeyBulkRequest{IDs: body.IDs, Action: domain.KeyBulkAction(body.Action)}
	move, limits := request.Action == domain.KeyBulkMoveGroup, request.Action == domain.KeyBulkSetLimits
	if move != (body.GroupID != nil) || limits != (body.Limits != nil) {
		return refuse("groupId belongs to move_group and limits to set_limits")
	}
	if move && string(body.GroupID) != "null" {
		var group string
		if err := json.Unmarshal(body.GroupID, &group); err != nil {
			return refuse("groupId must be a group id or null")
		}
		request.GroupID = &group
	}
	if limits {
		request.Limits = make([]domain.LimitRule, 0, len(*body.Limits))
		for _, rule := range *body.Limits {
			request.Limits = append(request.Limits, domain.LimitRule{Type: rule.Type, Window: rule.Window,
				ModelFilter: rule.ModelFilter, MaxValue: rule.MaxValue})
		}
	}
	return request, true
}

func (s *Server) failKeys(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, application.ErrKeyFilters):
		writeError(w, http.StatusBadRequest, "invalid_key_filters", "Invalid keys filter, sort or page")
	case errors.Is(err, application.ErrBulkRequest):
		writeError(w, http.StatusBadRequest, "invalid_bulk_request", "Invalid bulk key request: 1 to 1000 distinct ids and a known action")
	case errors.Is(err, application.ErrBulkLimits):
		writeError(w, http.StatusUnprocessableEntity, "invalid_limit", "Invalid limit configuration")
	case errors.Is(err, domain.ErrReportTooLarge):
		writeError(w, http.StatusBadRequest, "too_many_keys", "Too many keys to list")
	default:
		s.fail(w, err)
	}
}
