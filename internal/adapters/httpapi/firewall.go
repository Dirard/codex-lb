package httpapi

import (
	"errors"
	"net/http"
	"net/netip"
	"strings"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

func (s *Server) ConfigureFirewall(f *application.Firewall) { s.firewall = f }

func (s *Server) registerFirewallRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/firewall/ips", func(w http.ResponseWriter, r *http.Request) {
		entries := s.firewall.Entries()
		mode := "allow_all"
		if len(entries) > 0 {
			mode = "allowlist_active"
		}
		writeJSON(w, 200, struct {
			Mode    string                 `json:"mode"`
			Entries []domain.FirewallEntry `json:"entries"`
		}{mode, entries})
	})
	mux.HandleFunc("POST /api/firewall/ips", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			IPAddress string `json:"ipAddress"`
		}
		if !decodeJSON(w, r, &body) {
			return
		}
		entry, err := s.firewall.Add(r.Context(), body.IPAddress)
		if err != nil {
			s.firewallError(w, err)
			return
		}
		writeJSON(w, 201, entry)
	})
	mux.HandleFunc("DELETE /api/firewall/ips/{ip}", func(w http.ResponseWriter, r *http.Request) {
		if err := s.firewall.Delete(r.Context(), r.PathValue("ip")); err != nil {
			s.firewallError(w, err)
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ok"})
	})
}

func (s *Server) firewallError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalid):
		writeError(w, 400, "invalid_ip", "Expected an IPv4 or IPv6 address")
	case errors.Is(err, domain.ErrConflict):
		writeError(w, 409, "ip_exists", "IP address already exists")
	case errors.Is(err, domain.ErrNotFound):
		writeError(w, 404, "ip_not_found", "IP address was not found")
	default:
		s.fail(w, err)
	}
}

func firewallIdentity(r *http.Request, trusted []netip.Prefix) netip.Addr {
	// Firewall identity ignores singleton vendor headers. Reuse the exact chain
	// parser used for bootstrap/session security, without trusting these aliases.
	request := *r
	request.Header = make(http.Header, 2)
	for _, name := range []string{"X-Forwarded-For", "Forwarded"} {
		request.Header[name] = r.Header.Values(name)
	}
	id := ResolveIdentity(&request, trusted)
	if id.TrustedPeer && !id.Forwarded {
		return netip.Addr{}
	}
	return id.IP
}

func proxyFacingPath(path string) bool {
	return strings.HasPrefix(path, "/v1/") || strings.HasPrefix(path, "/backend-api/") || strings.HasPrefix(path, "/api/codex/")
}
