package application

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync"
)

// BootstrapTokenFile publishes a generated one-time setup token where only the
// operator of the host can read it.
type BootstrapTokenFile interface {
	Publish(token string) error
	Withdraw() error
	Location() string
}

// Bootstrap token sources reported to the dashboard.
const (
	BootstrapSourceEnvironment = "environment"
	BootstrapSourceFile        = "file"
)

var errBootstrapToken = &AuthError{Code: "invalid_bootstrap_token", Message: "A valid bootstrap token is required for setup"}

// bootstrapGate holds the tokens that may authorize first-run password setup.
// Only SHA-256 digests are kept in memory after configuration.
type bootstrapGate struct {
	mu               sync.Mutex
	operator         [sha256.Size]byte
	hasOperator      bool
	operatorConsumed bool
	generated        [sha256.Size]byte
	hasGenerated     bool
	file             BootstrapTokenFile
	logger           *slog.Logger
}

// ConfigureBootstrap supplies the operator token (may be empty) and the file
// used for generated tokens (may be nil, which disables generation).
func (a *AdminAuth) ConfigureBootstrap(operatorToken string, file BootstrapTokenFile, logger *slog.Logger) {
	a.bootstrap.mu.Lock()
	defer a.bootstrap.mu.Unlock()
	a.bootstrap.hasOperator = operatorToken != ""
	a.bootstrap.operator = sha256.Sum256([]byte(operatorToken))
	a.bootstrap.file = file
	a.bootstrap.logger = logger
}

// PrepareBootstrap arms setup for an installation without a dashboard password
// and withdraws a stale generated token once a password exists.
func (a *AdminAuth) PrepareBootstrap(ctx context.Context) error {
	secret, err := a.settings.LoadAdminSecret(ctx)
	if err != nil {
		return err
	}
	a.bootstrap.mu.Lock()
	defer a.bootstrap.mu.Unlock()
	if a.bootstrap.hasOperator {
		consumed, err := a.sessions.BootstrapTokenConsumed(ctx, bootstrapDigest(a.bootstrap.operator))
		if err != nil {
			return err
		}
		a.bootstrap.operatorConsumed = consumed
	}
	if secret.PasswordHash != "" {
		a.bootstrap.hasGenerated = false
		if a.bootstrap.file != nil {
			return a.bootstrap.file.Withdraw()
		}
		return nil
	}
	if a.bootstrap.hasOperator && !a.bootstrap.operatorConsumed {
		a.bootstrap.log("dashboard setup requires the bootstrap token configured by CODEX_LB_DASHBOARD_BOOTSTRAP_TOKEN")
		if a.bootstrap.file != nil {
			return a.bootstrap.file.Withdraw()
		}
		return nil
	}
	return a.bootstrap.generateLocked()
}

// BootstrapSource reports which token kind currently authorizes setup.
func (a *AdminAuth) BootstrapSource() string {
	a.bootstrap.mu.Lock()
	defer a.bootstrap.mu.Unlock()
	switch {
	case a.bootstrap.hasOperator && !a.bootstrap.operatorConsumed:
		return BootstrapSourceEnvironment
	case a.bootstrap.hasGenerated:
		return BootstrapSourceFile
	default:
		return ""
	}
}

func (g *bootstrapGate) generateLocked() error {
	if g.file == nil {
		g.hasGenerated = false
		return nil
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return err
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	if err := g.file.Publish(token); err != nil {
		g.hasGenerated = false
		return fmt.Errorf("publish dashboard bootstrap token: %w", err)
	}
	g.generated = sha256.Sum256([]byte(token))
	g.hasGenerated = true
	g.log("dashboard setup requires the one-time bootstrap token written to the data directory", "path", g.file.Location())
	return nil
}

func (g *bootstrapGate) log(message string, attrs ...any) {
	if g.logger != nil {
		g.logger.Warn(message, attrs...)
	}
}

// match reports whether supplied equals an armed token, comparing every armed
// digest in constant time; operator is true when the operator token matched.
func (g *bootstrapGate) match(supplied string) (operator bool, ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	digest := sha256.Sum256([]byte(supplied))
	operatorMatch := subtle.ConstantTimeCompare(digest[:], g.operator[:]) == 1 && g.hasOperator && !g.operatorConsumed
	generatedMatch := subtle.ConstantTimeCompare(digest[:], g.generated[:]) == 1 && g.hasGenerated
	return operatorMatch, operatorMatch || generatedMatch
}

// consume invalidates the token used for a successful setup.
func (a *AdminAuth) consumeBootstrap(ctx context.Context, operator bool) error {
	a.bootstrap.mu.Lock()
	defer a.bootstrap.mu.Unlock()
	if operator {
		a.bootstrap.operatorConsumed = true
		return a.sessions.ConsumeBootstrapToken(ctx, bootstrapDigest(a.bootstrap.operator), a.now())
	}
	a.bootstrap.hasGenerated = false
	if a.bootstrap.file != nil {
		return a.bootstrap.file.Withdraw()
	}
	return nil
}

// rearmBootstrap publishes a fresh generated token after the password was
// removed; a consumed operator token is never accepted again.
func (a *AdminAuth) rearmBootstrap() error {
	a.bootstrap.mu.Lock()
	defer a.bootstrap.mu.Unlock()
	if a.bootstrap.hasOperator && !a.bootstrap.operatorConsumed {
		return nil
	}
	return a.bootstrap.generateLocked()
}

func bootstrapDigest(digest [sha256.Size]byte) string { return hex.EncodeToString(digest[:]) }
