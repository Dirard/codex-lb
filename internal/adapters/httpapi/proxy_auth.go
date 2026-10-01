package httpapi

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"codex-lb/internal/domain"
)

type localProxyAccess struct{}

func (s *Server) proxyIngress(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity := ResolveIdentity(r, s.config.TrustedProxies)
		local := identity.Local && !identity.Forwarded
		peer, err := netip.ParseAddrPort(r.RemoteAddr)
		if err == nil && containsIP(s.config.UnauthenticatedClientCIDRs, peer.Addr().Unmap()) {
			local = true
		}
		if local {
			r = r.WithContext(context.WithValue(r.Context(), localProxyAccess{}, true))
		}
		next.ServeHTTP(w, r)
	})
}

func authenticateProxyKey(r *http.Request, store ProxyRepository) (domain.APIKey, error) {
	usagePath := strings.TrimSuffix(r.URL.Path, "/")
	if allowed, _ := r.Context().Value(localProxyAccess{}).(bool); allowed && len(r.Header.Values("X-Codex-Lb-Required-Capability")) == 0 && usagePath != "/v1/usage" && usagePath != "/api/codex/usage" {
		settings, err := store.LoadSettings(r.Context())
		if err != nil {
			return domain.APIKey{}, err
		}
		if !settings.APIKeyAuthEnabled {
			return store.GetAPIKey(r.Context(), domain.LocalProxyKeyID)
		}
	}
	return authenticateBearerKey(r, store)
}

// authenticateBearerKey never substitutes a local or administrator principal.
func authenticateBearerKey(r *http.Request, store ProxyRepository) (domain.APIKey, error) {
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		return domain.APIKey{}, invalidKey()
	}
	parts := strings.Fields(values[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || len(parts[1]) > 4096 {
		return domain.APIKey{}, invalidKey()
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(parts[1])))
	key, err := store.FindAPIKeyByHash(r.Context(), hash)
	if errors.Is(err, domain.ErrNotFound) || err == nil && (!key.IsActive || key.ExpiresAt != nil && !key.ExpiresAt.After(time.Now())) {
		return domain.APIKey{}, invalidKey()
	}
	return key, err
}
