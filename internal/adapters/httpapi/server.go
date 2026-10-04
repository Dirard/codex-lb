// Package httpapi exposes transport contracts; policy and storage live behind
// application use cases and ports.
package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/netip"
	"strings"
	"sync"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

type Repository interface {
	application.Accounts
	application.AccountQuotaMetadata
	application.Groups
	application.APIKeys
	application.UsageLedger
	application.Settings
	application.ReportsRepository
	application.AffinityRepository
}

type Config struct {
	TrustedProxies             []netip.Prefix
	BootstrapToken             string
	Version                    string
	ConnectAddress             string
	DashboardAuthMode          string
	DashboardAuthHeader        string
	UnauthenticatedClientCIDRs []netip.Prefix
}

type Server struct {
	store          Repository
	auth           *application.AdminAuth
	cipher         application.SecretCipher
	config         Config
	logger         *slog.Logger
	accounts       *application.AccountsService
	usage          *application.AccountUsageService
	automations    *application.AutomationsService
	warmups        *application.WarmupService
	catalog        *application.ModelCatalogService
	modelPricing   *ModelPricingHandler
	firewall       *application.Firewall
	archives       *application.ErrorArchives
	archiveRepo    application.ErrorArchiveRepository
	runtimeUpdates application.RuntimeUpdater
	accountsOnce   sync.Once
}

func New(store Repository, cipher application.SecretCipher, config Config, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Server{store: store, auth: application.NewAdminAuth(store, cipher), cipher: cipher, config: config, logger: logger}
}

// ConfigureUsage supplies the account telemetry/reset-credit use case before
// Handler is built; it performs no provider requests itself.
func (s *Server) ConfigureUsage(usage *application.AccountUsageService) { s.usage = usage }

func (s *Server) ConfigureAutomations(service *application.AutomationsService) {
	s.automations = service
}

func (s *Server) ConfigureWarmups(service *application.WarmupService) { s.warmups = service }

func (s *Server) ConfigureModelCatalog(service *application.ModelCatalogService) { s.catalog = service }

func (s *Server) ConfigureModelPricing(store application.ModelPricingStore) {
	s.modelPricing = NewModelPricingHandler(store)
}

// ConfigureRuntimeUpdater supplies the managed runtime controller before Handler is built.
func (s *Server) ConfigureRuntimeUpdater(updater application.RuntimeUpdater) {
	s.runtimeUpdates = updater
}

// Handler composes the admin API with the already-wired proxy and embedded UI.
// Missing transports are not silently replaced with successful stub responses.
func (s *Server) Handler(proxy, ui http.Handler) http.Handler {
	mux := http.NewServeMux()
	s.registerAuth(mux)
	s.registerKeyReportSession(mux)
	admin := http.NewServeMux()
	s.registerAdmin(admin)
	s.registerReportsRoutes(admin, s.store)
	s.registerAffinityRoutes(admin)
	if s.modelPricing != nil {
		s.modelPricing.RegisterAdminRoutes(admin)
	}
	if s.catalog != nil {
		NewModelCatalogHandler(s.store, s.catalog).RegisterAdminRoutes(admin)
	}
	if s.archives != nil {
		s.registerErrorArchiveRoutes(admin)
	}
	if s.firewall != nil {
		s.registerFirewallRoutes(admin)
	}
	s.registerRuntimeUpdateRoutes(admin)
	if s.accounts != nil {
		s.registerAccountRoutes(admin)
	}
	if s.usage != nil {
		s.registerAccountUsageRoutes(admin, s.usage)
	}
	if s.automations != nil {
		s.registerAutomationsRoutes(admin, s.automations)
	}
	if s.warmups != nil {
		s.registerWarmupRoutes(admin, s.warmups)
	}
	s.registerModelSourceRoutes(admin)
	mux.Handle("/api/", s.requireAdmin(admin))
	if proxy != nil {
		proxy = s.proxyIngress(proxy)
		mux.Handle("/v1/", proxy)
		mux.Handle("/backend-api/", proxy)
		mux.Handle("/api/codex/", proxy)
	}
	if ui != nil {
		mux.Handle("/", ui)
	}
	csrf := http.NewCrossOriginProtection()
	csrf.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusForbidden, "cross_origin_request", "Cross-origin request rejected")
	}))
	protected := csrf.Handler(mux)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-Frame-Options", "DENY")
		if len(r.URL.Path) >= 5 && r.URL.Path[:5] == "/api/" {
			w.Header().Set("Cache-Control", "no-store")
		}
		if s.authMode() == "disabled" && (strings.HasPrefix(r.URL.Path, "/api/dashboard-auth/password") || strings.HasPrefix(r.URL.Path, "/api/dashboard-auth/totp")) {
			writeError(w, http.StatusBadRequest, "password_management_disabled", "Password and TOTP management are disabled by the installation authentication mode")
			return
		}
		if s.firewall != nil && (proxyFacingPath(r.URL.Path) || keyReportFirewallPath(r)) && !s.firewall.Allowed(firewallIdentity(r, s.config.TrustedProxies)) {
			writeError(w, http.StatusForbidden, "ip_forbidden", "Client IP is not in the proxy allowlist")
			return
		}
		protected.ServeHTTP(w, r)
	})
}

func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := s.authorizeDashboard(r); err != nil {
			s.fail(w, err)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || kind != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "invalid_content_type", "Content-Type must be application/json")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Invalid JSON request")
		return false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_request", "Expected one JSON object")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	type detail struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	writeJSON(w, status, struct {
		Error detail `json:"error"`
	}{detail{code, message}})
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	for _, known := range []struct {
		target        error
		status        int
		code, message string
	}{
		{domain.ErrNotFound, http.StatusNotFound, "not_found", "The requested resource does not exist"},
		{domain.ErrConflict, http.StatusConflict, "conflict", "The resource changed or already exists"},
		{domain.ErrInvalid, http.StatusUnprocessableEntity, "invalid_request", "Invalid settings or resource data"},
		{domain.ErrInvalidLimit, http.StatusUnprocessableEntity, "invalid_limit", "Invalid limit configuration"},
	} {
		if errors.Is(err, known.target) {
			writeError(w, known.status, known.code, known.message)
			return
		}
	}
	var auth *application.AuthError
	if errors.As(err, &auth) {
		status := http.StatusUnauthorized
		switch auth.Code {
		case "password_already_configured":
			status = http.StatusConflict
		case "invalid_password", "password_too_long":
			status = http.StatusUnprocessableEntity
		case "invalid_totp_setup", "invalid_totp_code", "password_not_configured":
			status = http.StatusBadRequest
		}
		if auth.RetryAfter > 0 {
			status = http.StatusTooManyRequests
			w.Header().Set("Retry-After", formatInt(int64(auth.RetryAfter)))
		}
		writeError(w, status, auth.Code, auth.Message)
		return
	}
	// Do not log an arbitrary error string: upstream/database errors may contain
	// credentials or user content. The operation name belongs in structured logs.
	s.logger.Error("admin operation failed", "error_type", errorType(err))
	writeError(w, http.StatusInternalServerError, "internal_error", "The operation could not be completed")
}
