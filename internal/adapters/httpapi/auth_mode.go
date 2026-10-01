package httpapi

import (
	"errors"
	"net/http"
	"net/netip"
	"strings"

	"codex-lb/internal/application"
)

func ValidateConfig(config Config) error {
	if address := strings.TrimSpace(config.ConnectAddress); address != "" && !safeConnectAddress(address) {
		return errors.New("CODEX_LB_CONNECT_ADDRESS must be a hostname or IP address")
	}
	switch config.DashboardAuthMode {
	case "", "standard", "disabled":
		return nil
	case "trusted_header":
		if len(config.TrustedProxies) == 0 {
			return errors.New("trusted_header authentication requires trusted proxy CIDRs")
		}
		if !httpToken(config.DashboardAuthHeader) {
			return errors.New("dashboard auth header must be a valid non-reserved header name")
		}
		for _, name := range []string{"authorization", "connection", "content-length", "content-type", "cookie", "forwarded", "host", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade", "x-forwarded-for", "x-forwarded-host", "x-forwarded-proto", "x-real-ip", "true-client-ip", "cf-connecting-ip", "origin", "referer", "sec-fetch-site"} {
			if strings.EqualFold(name, config.DashboardAuthHeader) {
				return errors.New("reserved dashboard authentication header")
			}
		}
		return nil
	default:
		return errors.New("dashboard authentication mode must be standard, trusted_header, or disabled")
	}
}

func (s *Server) authMode() string {
	if s.config.DashboardAuthMode == "" {
		return "standard"
	}
	return s.config.DashboardAuthMode
}

func (s *Server) alternativeAdmin(r *http.Request) bool {
	if s.authMode() == "disabled" {
		return true
	}
	if s.authMode() != "trusted_header" || ValidateConfig(s.config) != nil {
		return false
	}
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil || !containsIP(s.config.TrustedProxies, peer.Addr().Unmap()) {
		return false
	}
	values := r.Header.Values(s.config.DashboardAuthHeader)
	if len(values) != 1 {
		return false
	}
	actor := strings.TrimSpace(values[0])
	if actor == "" || len(actor) > 1024 {
		return false
	}
	for _, character := range actor {
		if character < 32 || character == 127 {
			return false
		}
	}
	return true
}

func (s *Server) authorizeDashboard(r *http.Request) error {
	if s.alternativeAdmin(r) {
		return nil
	}
	return s.auth.Authorize(r.Context(), sessionToken(r))
}

func (s *Server) dashboardState(r *http.Request, token string) (application.AuthState, error) {
	state, err := s.auth.State(r.Context(), token)
	if err != nil {
		return state, err
	}
	state.AuthMode = s.authMode()
	state.PasswordManagement = s.authMode() != "disabled"
	if s.alternativeAdmin(r) {
		state.Authenticated = true
		state.BootstrapRequired = false
		state.PasswordRequired = false
		state.TOTPRequiredOnLogin = false
	}
	return state, nil
}
