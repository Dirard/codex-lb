package application

import (
	"math"
	"net/netip"
	"sync"
	"time"
)

// Authentication scopes share neither per-client windows nor installation-wide
// failure counters, so a TOTP attack cannot lock password login and vice versa.
const (
	authScopePassword  = "password"
	authScopeTOTP      = "totp"
	authScopeBootstrap = "bootstrap"
)

const (
	authClientAttempts   = 8
	authClientWindow     = time.Minute
	authClientCapacity   = 4096
	authSharedFreeErrors = 10
	authSharedQuiet      = 15 * time.Minute
	authSharedMaxDelay   = 15 * time.Minute
)

type authClientWindowState struct {
	start time.Time
	count int
}

type authSharedFailures struct {
	count int
	last  time.Time
}

// authLimiter bounds attempts per client and failures per installation. It
// holds only counters; password hashing and storage I/O never run under mu.
type authLimiter struct {
	mu      sync.Mutex
	clients map[string]authClientWindowState
	shared  map[string]authSharedFailures
}

func newAuthLimiter() *authLimiter {
	return &authLimiter{clients: make(map[string]authClientWindowState), shared: make(map[string]authSharedFailures)}
}

// begin admits one attempt or returns a rate-limit error with Retry-After.
func (l *authLimiter) begin(scope, client string, now time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	shared := l.shared[scope]
	if !shared.last.IsZero() && now.Sub(shared.last) >= authSharedQuiet {
		delete(l.shared, scope)
		shared = authSharedFailures{}
	}
	if shared.count >= authSharedFreeErrors {
		if until := shared.last.Add(sharedFailureDelay(shared.count)); now.Before(until) {
			return authRateLimited(until.Sub(now))
		}
	}
	for key, state := range l.clients {
		if !now.Before(state.start.Add(authClientWindow)) {
			delete(l.clients, key)
		}
	}
	key := scope + "|" + authClientKey(client)
	state, exists := l.clients[key]
	if !exists && len(l.clients) >= authClientCapacity {
		return authRateLimited(authClientWindow)
	}
	if state.count >= authClientAttempts {
		return authRateLimited(state.start.Add(authClientWindow).Sub(now))
	}
	if !exists {
		state.start = now
	}
	state.count++
	l.clients[key] = state
	return nil
}

// fail records a rejected secret for the whole installation.
func (l *authLimiter) fail(scope string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	shared := l.shared[scope]
	shared.count++
	shared.last = now
	l.shared[scope] = shared
}

// succeed clears only the caller's window. The shared counter decays with
// time so an attacker cannot reset it by interleaving a legitimate login.
func (l *authLimiter) succeed(scope, client string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.clients, scope+"|"+authClientKey(client))
}

func sharedFailureDelay(count int) time.Duration {
	exponent := count - authSharedFreeErrors
	if exponent >= 10 {
		return authSharedMaxDelay
	}
	return min(time.Duration(math.Pow(2, float64(exponent)))*time.Second, authSharedMaxDelay)
}

func authRateLimited(wait time.Duration) error {
	seconds := int(math.Ceil(wait.Seconds()))
	return &AuthError{Code: "login_rate_limited", Message: "Too many authentication attempts", RetryAfter: max(1, seconds)}
}

// authClientKey groups IPv6 clients by /64 because one subscriber usually
// controls the whole prefix; IPv4 addresses and unparsable values stay exact.
func authClientKey(client string) string {
	addr, err := netip.ParseAddr(client)
	if err != nil {
		return client
	}
	addr = addr.Unmap()
	if addr.Is6() {
		return netip.PrefixFrom(addr.WithZone(""), 64).Masked().String()
	}
	return addr.String()
}
