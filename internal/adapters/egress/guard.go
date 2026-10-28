// Package egress restricts where operator-configured upstreams may connect.
// Requests whose context is marked with Restrict reach only public unicast
// addresses; the decision is made on resolved addresses at dial time, so a
// DNS change cannot redirect a stored destination to an internal service.
package egress

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sync"

	"codex-lb/internal/domain"
)

// ErrForbiddenDestination is returned for a restricted connection whose
// destination resolves to a non-public address.
var ErrForbiddenDestination = errors.New("destination is not a public address")

type restrictedKey struct{}

// Restrict marks ctx so connections made for it may reach only public addresses.
func Restrict(ctx context.Context) context.Context {
	return context.WithValue(ctx, restrictedKey{}, true)
}

// Restricted reports whether ctx was marked by Restrict.
func Restricted(ctx context.Context) bool {
	value, _ := ctx.Value(restrictedKey{}).(bool)
	return value
}

// DialFunc matches net.Dialer.DialContext and http.Transport.DialContext.
type DialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// Guard enforces the destination policy for restricted contexts. Unmarked
// contexts (subscription, OAuth and release traffic) are passed through.
type Guard struct {
	allowPrivate bool
	lookup       func(ctx context.Context, host string) ([]netip.Addr, error)
	logger       *slog.Logger
	// proxies holds environment proxy endpoints already returned for a
	// restricted request; the destination behind them was checked by name.
	proxies sync.Map
	warned  sync.Map
}

// New returns a guard; allowPrivate disables the restriction entirely.
func New(allowPrivate bool, logger *slog.Logger) *Guard {
	return &Guard{allowPrivate: allowPrivate, logger: logger, lookup: func(ctx context.Context, host string) ([]netip.Addr, error) {
		return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	}}
}

// DialContext resolves restricted destinations itself and dials a checked IP,
// so no second resolution can substitute another address.
func (g *Guard) DialContext(dial DialFunc) DialFunc {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		if g.allowPrivate || !Restricted(ctx) {
			return dial(ctx, network, address)
		}
		if _, proxy := g.proxies.Load(address); proxy {
			return dial(ctx, network, address)
		}
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		addresses, err := g.publicAddresses(ctx, host)
		if err != nil {
			return nil, err
		}
		var dialErr error
		for _, ip := range addresses {
			conn, err := dial(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			dialErr = err
		}
		return nil, dialErr
	}
}

// Proxy wraps an environment proxy function: before a restricted request is
// handed to a proxy, its destination name must resolve to public addresses.
func (g *Guard) Proxy(next func(*http.Request) (*url.URL, error)) func(*http.Request) (*url.URL, error) {
	return func(request *http.Request) (*url.URL, error) {
		if next == nil {
			return nil, nil
		}
		proxy, err := next(request)
		if err != nil || proxy == nil || g.allowPrivate || !Restricted(request.Context()) {
			return proxy, err
		}
		if _, err := g.publicAddresses(request.Context(), request.URL.Hostname()); err != nil {
			return nil, err
		}
		g.proxies.Store(proxyEndpoint(proxy), true)
		return proxy, nil
	}
}

func (g *Guard) publicAddresses(ctx context.Context, host string) ([]netip.Addr, error) {
	var addresses []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		addresses = []netip.Addr{ip}
	} else {
		addresses, err = g.lookup(ctx, host)
		if err != nil {
			return nil, err
		}
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("%w: %s has no address", ErrForbiddenDestination, host)
	}
	// A mixed answer is refused as a whole: dialing only its public part would
	// still let a resolver decide when the private part is used.
	for _, ip := range addresses {
		if !domain.PublicUnicast(ip) {
			g.warn(host)
			return nil, fmt.Errorf("%w: %s", ErrForbiddenDestination, host)
		}
	}
	return addresses, nil
}

func (g *Guard) warn(host string) {
	if g.logger == nil {
		return
	}
	if _, seen := g.warned.LoadOrStore(host, true); !seen {
		g.logger.Warn("model source connection refused: destination is not a public address; use --allow-private-model-sources for self-hosted sources", "host", host)
	}
}

func proxyEndpoint(proxy *url.URL) string {
	port := proxy.Port()
	if port == "" {
		switch proxy.Scheme {
		case "https":
			port = "443"
		case "socks5", "socks5h":
			port = "1080"
		default:
			port = "80"
		}
	}
	return net.JoinHostPort(proxy.Hostname(), port)
}
