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
	"sync/atomic"

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
	application.DashboardSessions
}

type Config struct {
	TrustedProxies []netip.Prefix
	BootstrapToken string
	// BootstrapTokenFile publishes generated one-time setup tokens; nil
	// disables generation, so setup then needs BootstrapToken.
	BootstrapTokenFile         application.BootstrapTokenFile
	Version                    string
	ConnectAddress             string
	DashboardAuthMode          string
	DashboardAuthHeader        string
	UnauthenticatedClientCIDRs []netip.Prefix
	// PublicOrigin is the HTTPS origin browsers use for the dashboard. It
	// enables Secure __Host- cookies and HSTS independently of proxy headers.
	PublicOrigin string
	// AllowPrivateModelSources permits model-source base URLs on non-public
	// addresses for self-hosted sources.
	AllowPrivateModelSources bool
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
	capacity       *RuntimeCapacitySource
	accountsOnce   sync.Once
	// forwardingWarned limits the untrusted-forwarding warning to one record.
	forwardingWarned atomic.Bool
}

func New(store Repository, cipher application.SecretCipher, config Config, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	auth := application.NewAdminAuth(store, cipher)
	auth.ConfigureBootstrap(config.BootstrapToken, config.BootstrapTokenFile, logger)
	return &Server{store: store, auth: auth, cipher: cipher, config: config, logger: logger}
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
	if s.capacity != nil {
		s.registerRuntimeCapacityRoutes(admin)
	}
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
	if s.config.PublicOrigin != "" {
		// Validated as a bare https origin by ValidateConfig.
		_ = csrf.AddTrustedOrigin(s.config.PublicOrigin)
	}
	csrf.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusForbidden, "cross_origin_request", "Cross-origin request rejected")
	}))
	protected := csrf.Handler(mux)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.warnUntrustedForwarding(r)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-Frame-Options", "DENY")
		if proxyFacingPath(r.URL.Path) {
			w.Header().Set("Content-Security-Policy", proxyContentSecurityPolicy)
		} else {
			w.Header().Set("Content-Security-Policy", dashboardContentSecurityPolicy)
		}
		if s.config.PublicOrigin != "" || r.TLS != nil {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}
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
		renewal, err := s.authorizeDashboard(r)
		if err != nil {
			s.fail(w, err)
			return
		}
		if renewal.Token != "" {
			s.cookie(w, r, renewal.Token, int(renewal.MaxAge.Seconds()))
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
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "Request body is too large")
			return false
		}
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
		case "invalid_bootstrap_token", "reauthentication_required":
			status = http.StatusForbidden
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

// The dashboard bundle loads only same-origin scripts; inline styles remain
// allowed for component libraries that set style attributes.
const dashboardContentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self'; object-src 'none'; " +
	"base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

// Proxy responses are API payloads; a browser must never render them as a page.
const proxyContentSecurityPolicy = "default-src 'none'; frame-ancestors 'none'; sandbox"

// warnUntrustedForwarding reports once that a same-host proxy forwards client
// identity without being trusted, which merges every client into one address.
func (s *Server) warnUntrustedForwarding(r *http.Request) {
	if s.forwardingWarned.Load() {
		return
	}
	id := ResolveIdentity(r, s.config.TrustedProxies)
	if !id.Forwarded || id.TrustedPeer || !id.IP.IsLoopback() {
		return
	}
	if s.forwardingWarned.CompareAndSwap(false, true) {
		s.logger.Warn("forwarded client headers from an untrusted loopback peer are ignored; list the reverse proxy with --trusted-proxy", "peer", id.IP.String())
	}
}
