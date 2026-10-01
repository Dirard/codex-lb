package httpapi

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

// registerAccountRoutes registers the account admin/OAuth API. Callers wrap
// it with requireAdmin; listAccounts stays in admin.go.
func (s *Server) registerAccountRoutes(mux *http.ServeMux) {
	mux.HandleFunc("PATCH /api/accounts/{id}", s.updateAccount)
	mux.HandleFunc("POST /api/accounts/import", s.importAccount)
	mux.HandleFunc("POST /api/accounts/{id}/pause", s.pauseAccount)
	mux.HandleFunc("POST /api/accounts/{id}/reactivate", s.reactivateAccount)
	mux.HandleFunc("PUT /api/accounts/{id}/alias", s.setAccountAlias)
	mux.HandleFunc("PUT /api/accounts/{id}/limit-warmup", s.updateAccountLimitWarmup)
	mux.HandleFunc("PUT /api/accounts/{id}/routing-policy", s.updateAccountRoutingPolicy)
	mux.HandleFunc("DELETE /api/accounts/{id}", s.deleteAccount)
	mux.HandleFunc("POST /api/accounts/{id}/export/auth", s.exportAccountAuth)
	mux.HandleFunc("POST /api/oauth/start", s.startOauth)
	mux.HandleFunc("GET /api/oauth/status", s.oauthStatus)
	mux.HandleFunc("POST /api/oauth/complete", s.completeOauth)
	mux.HandleFunc("POST /api/oauth/manual-callback", s.manualOauthCallback)
}

func (s *Server) accountsService() *application.AccountsService {
	return s.accounts
}

func (s *Server) setAccountsService(service *application.AccountsService) {
	if err := s.ConfigureAccounts(service); err != nil {
		panic(err)
	}
}

// ConfigureAccounts is a composition-root operation, before serving requests.
func (s *Server) ConfigureAccounts(service *application.AccountsService) error {
	if service == nil {
		return errors.New("account service is required")
	}
	applied := false
	s.accountsOnce.Do(func() { s.accounts = service; applied = true })
	if !applied {
		return errors.New("account service already configured")
	}
	return nil
}

func (s *Server) updateAccount(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		SecurityWorkAuthorized *bool `json:"securityWorkAuthorized"`
	}
	if !decodeJSON(w, r, &payload) {
		return
	}
	if err := s.accountsService().UpdateAccount(r.Context(), r.PathValue("id"), payload.SecurityWorkAuthorized); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Status string `json:"status"`
	}{"updated"})
}

func (s *Server) pauseAccount(w http.ResponseWriter, r *http.Request) {
	if err := s.accountsService().PauseAccount(r.Context(), r.PathValue("id")); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Status string `json:"status"`
	}{"paused"})
}

func (s *Server) reactivateAccount(w http.ResponseWriter, r *http.Request) {
	if err := s.accountsService().ReactivateAccount(r.Context(), r.PathValue("id")); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Status string `json:"status"`
	}{"reactivated"})
}

func (s *Server) setAccountAlias(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Alias *string `json:"alias"`
	}
	if !decodeJSON(w, r, &payload) {
		return
	}
	accountID, alias, err := s.accountsService().SetAlias(r.Context(), r.PathValue("id"), payload.Alias)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		AccountID string  `json:"accountId"`
		Alias     *string `json:"alias"`
	}{accountID, alias})
}

func (s *Server) updateAccountLimitWarmup(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Enabled bool `json:"enabled"`
	}
	if !decodeJSON(w, r, &payload) {
		return
	}
	if err := s.accountsService().SetLimitWarmup(r.Context(), r.PathValue("id"), payload.Enabled); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Status  string `json:"status"`
		Enabled bool   `json:"enabled"`
	}{statusEnabled(payload.Enabled), payload.Enabled})
}

func (s *Server) updateAccountRoutingPolicy(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		RoutingPolicy string `json:"routingPolicy"`
	}
	if !decodeJSON(w, r, &payload) {
		return
	}
	accountID := r.PathValue("id")
	if err := s.accountsService().SetRoutingPolicy(r.Context(), accountID, payload.RoutingPolicy); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		AccountID     string `json:"accountId"`
		RoutingPolicy string `json:"routingPolicy"`
	}{accountID, payload.RoutingPolicy})
}

func (s *Server) deleteAccount(w http.ResponseWriter, r *http.Request) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	deleteHistory := false
	if values, present := query["delete_history"]; present && err == nil {
		if len(values) != 1 {
			writeError(w, http.StatusBadRequest, "invalid_request", "delete_history must be one boolean value")
			return
		}
		deleteHistory, err = strconv.ParseBool(values[0])
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "delete_history must be a boolean")
		return
	}
	if err := s.accountsService().DeleteAccount(r.Context(), r.PathValue("id"), deleteHistory); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Status string `json:"status"`
	}{"deleted"})
}

const maxAuthJSONSize = 1 << 20

func (s *Server) importAccount(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxAuthJSONSize+(16<<10))
	if err := r.ParseMultipartForm(32 << 10); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_multipart", "Expected multipart/form-data with an auth_json file")
		return
	}
	file, _, err := r.FormFile("auth_json")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_multipart", "auth_json file is required")
		return
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxAuthJSONSize+1))
	if err != nil || len(raw) > maxAuthJSONSize {
		writeError(w, http.StatusBadRequest, "invalid_auth_json", "auth.json exceeds the 1 MiB limit")
		return
	}
	result, err := s.accountsService().ImportAccount(r.Context(), raw)
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrInvalid):
			writeError(w, http.StatusBadRequest, "invalid_auth_json", "Invalid auth.json payload")
		case errors.Is(err, domain.ErrConflict):
			writeError(w, http.StatusConflict, "duplicate_identity_conflict", "Multiple accounts match this identity")
		default:
			s.fail(w, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) exportAccountAuth(w http.ResponseWriter, r *http.Request) {
	result, err := s.accountsService().ExportAuth(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, private")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) startOauth(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		ForceMethod string  `json:"forceMethod"`
		AccountID   *string `json:"accountId"`
	}
	if !decodeJSON(w, r, &payload) {
		return
	}
	accountID := ""
	if payload.AccountID != nil {
		accountID = strings.TrimSpace(*payload.AccountID)
	}
	result, err := s.accountsService().StartOAuth(r.Context(), strings.TrimSpace(payload.ForceMethod), accountID)
	if err != nil {
		s.writeOAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) oauthStatus(w http.ResponseWriter, r *http.Request) {
	result := s.accountsService().OAuthStatus(r.Context(), r.URL.Query().Get("flowId"))
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) completeOauth(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		FlowID       string `json:"flowId"`
		DeviceAuthID string `json:"deviceAuthId"`
		UserCode     string `json:"userCode"`
	}
	if !decodeJSON(w, r, &payload) {
		return
	}
	result := s.accountsService().CompleteOAuth(r.Context(), payload.FlowID, payload.DeviceAuthID, payload.UserCode)
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) manualOauthCallback(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		CallbackURL string `json:"callbackUrl"`
		FlowID      string `json:"flowId"`
	}
	if !decodeJSON(w, r, &payload) {
		return
	}
	writeJSON(w, http.StatusOK, s.accountsService().ManualCallback(r.Context(), payload.CallbackURL, payload.FlowID))
}

func (s *Server) writeOAuthError(w http.ResponseWriter, err error) {
	var oauthErr *application.OAuthError
	if errors.As(err, &oauthErr) {
		code := oauthErr.Code
		if code == "" {
			code = "oauth_failed"
		}
		writeError(w, http.StatusBadGateway, code, oauthErr.Message)
		return
	}
	s.fail(w, err)
}

func statusEnabled(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}
