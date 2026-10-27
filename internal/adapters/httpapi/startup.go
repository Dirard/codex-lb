package httpapi

import (
	"context"
	"net/netip"
)

// Prepare arms first-run dashboard setup and reports configurations whose
// trust assumptions an operator should confirm. It must run before serving.
func (s *Server) Prepare(ctx context.Context) error {
	switch s.authMode() {
	case "standard":
		if err := s.auth.PrepareBootstrap(ctx); err != nil {
			return err
		}
	case "disabled":
		s.logger.Warn("dashboard authentication is disabled; every client that reaches the listener is an administrator")
	}
	for _, network := range s.config.UnauthenticatedClientCIDRs {
		if network.Addr().IsLoopback() || network.Contains(netip.AddrFrom4([4]byte{127, 0, 0, 1})) || network.Contains(netip.IPv6Loopback()) {
			s.logger.Warn("keyless proxy access is granted to loopback peers; a reverse proxy on this host must be listed with --trusted-proxy", "network", network.String())
		}
	}
	return nil
}
