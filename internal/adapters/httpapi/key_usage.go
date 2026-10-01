package httpapi

import (
	"context"
	"errors"
	"net/http"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

type KeyUsageStore interface {
	ProxyRepository
	application.KeyUsageRepository
	application.ReportsRepository
}

type KeyUsageHandler struct {
	store   KeyUsageStore
	service *application.KeyUsageService
	reports *application.ReportsService
}

func NewKeyUsageHandler(store KeyUsageStore) *KeyUsageHandler {
	return &KeyUsageHandler{store: store, service: application.NewKeyUsageService(store, nil), reports: application.NewReportsService(store, nil)}
}

func (h *KeyUsageHandler) RegisterPublicRoutes(mux *http.ServeMux) {
	for _, path := range []string{"/v1/usage", "/v1/usage/{$}"} {
		mux.HandleFunc("GET "+path, h.ServeSelfUsage)
	}
	for _, path := range []string{"/api/codex/usage", "/api/codex/usage/{$}"} {
		mux.HandleFunc("GET "+path, h.ServeCodexKeyUsage)
	}
	for _, path := range []string{"/v1/usage/reports", "/v1/usage/reports/{$}"} {
		mux.HandleFunc("GET "+path, h.serveReports)
	}
}

// ServeCodexKeyUsage is the API-key branch of /api/codex/usage. The caller
// decides whether this branch or provider-credential usage owns that request.
func (h *KeyUsageHandler) ServeCodexKeyUsage(w http.ResponseWriter, r *http.Request) {
	key, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	usage, err := h.service.CodexUsage(r.Context(), key.ID)
	if err != nil {
		h.fail(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, usage)
}

func (h *KeyUsageHandler) ServeSelfUsage(w http.ResponseWriter, r *http.Request) {
	key, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	usage, err := h.service.SelfUsage(r.Context(), key.ID)
	if err != nil {
		h.fail(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, usage)
}

func (h *KeyUsageHandler) authenticate(w http.ResponseWriter, r *http.Request) (domain.APIKey, bool) {
	key, err := authenticateBearerKey(r, h.store)
	if err != nil || domain.IsInternalKey(key.ID) {
		if err == nil {
			err = invalidKey()
		}
		writeProxyError(w, err)
		return domain.APIKey{}, false
	}
	return key, true
}

func (h *KeyUsageHandler) fail(w http.ResponseWriter, err error) {
	if errors.Is(err, domain.ErrNotFound) {
		err = invalidKey()
	}
	writeProxyError(w, err)
}

// QuotaHeaders provides a proxy integration hook; it never emits unscoped
// provider balance headers or bypasses this key's quota visibility policy.
func (h *KeyUsageHandler) QuotaHeaders(ctx context.Context, keyID string) (map[string]string, error) {
	return h.service.QuotaHeaders(ctx, keyID)
}
