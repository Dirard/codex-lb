package httpapi

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

func (s *Server) runtimeConnectAddress(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, struct {
		ConnectAddress string `json:"connectAddress"`
	}{resolveConnectAddress(ctx, r.Host, s.config.ConnectAddress, net.DefaultResolver.LookupNetIP)})
}

func resolveConnectAddress(ctx context.Context, host, override string, lookup func(context.Context, string, string) ([]netip.Addr, error)) string {
	const placeholder = "<codex-lb-ip-or-dns>"
	if address := strings.TrimSpace(override); address != "" {
		if !safeConnectAddress(address) {
			return placeholder
		}
		return address
	}
	if name, _, err := net.SplitHostPort(host); err == nil {
		host = name
	}
	host = strings.Trim(host, "[]")
	if !safeConnectAddress(host) || localHost(host) {
		return placeholder
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if ip.IsUnspecified() {
			return placeholder
		}
		return host
	}
	addresses, err := lookup(ctx, "ip4", host)
	if err == nil {
		for _, ip := range addresses {
			if ip.Is4() && !ip.IsLoopback() && !ip.IsUnspecified() {
				return ip.String()
			}
		}
	}
	return host
}

// The address is rendered inside a copyable netsh command, not only as text.
func safeConnectAddress(address string) bool {
	if len(address) == 0 || len(address) > 253 {
		return false
	}
	for _, char := range address {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune(".-_:[]", char)) {
			return false
		}
	}
	return true
}
