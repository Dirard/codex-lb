## Why

codex-lb is reachable from the internet through a reverse proxy on the same host, so a loopback socket peer with a loopback `Host` says nothing about who is calling. Treating such requests as local lets first-run setup skip the bootstrap token and, while API-key authentication is off, grants keyless proxy access. Authentication endpoints also accept large bodies and serialize password hashing behind one lock, limit failures only per client address, allow a configured TOTP to be optional, and issue sessions that cannot be revoked. Model-source base URLs can point at internal addresses, and the self-updater trusts a checksum file published next to the archive it describes.

## What Changes

- **BREAKING** First-run password setup always requires a bootstrap token. Without `CODEX_LB_DASHBOARD_BOOTSTRAP_TOKEN` (now 32–1024 characters) the server writes a one-time token to `<data-dir>/dashboard-bootstrap-token` (mode 0600) and logs the path; a used token is never accepted again.
- **BREAKING** Keyless proxy access (API-key auth disabled) is granted only to clients inside an explicit `--unauthenticated-client` network, resolved through trusted proxies. Socket or `Host` locality is no longer an authorization input. The dashboard shows a warning on every page while API-key auth is off.
- A WARN is logged for forwarded headers from an untrusted loopback peer; `--public-origin https://…` enables Secure `__Host-` cookies, HSTS and a trusted CSRF origin; the shipped unit trusts the loopback proxy.
- Authentication bodies are limited to 8 KiB; password hashing runs outside the state lock with at most four concurrent hashes and a fast 429.
- Failures are limited per client (IPv6 by /64) and installation-wide with exponential delay and `Retry-After`; rejections are logged without secrets.
- **BREAKING** A configured TOTP is required at every sign-in; the settings flag becomes a read-only mirror.
- **BREAKING** Dashboard sessions carry an id, epoch, idle deadline and absolute expiry (≤ 30 days, default 24 h); logout revokes the session, "sign out everywhere" advances the epoch, idle sessions expire after 12 hours. Earlier session cookies are rejected once.
- Model-source base URLs and every model-source connection are restricted to public addresses unless `--allow-private-model-sources` is set.
- Dashboard responses carry a Content-Security-Policy; account token export and runtime apply/rollback require the current password; `disabled` dashboard auth requires a loopback listener; error-archive redaction covers more credential formats.
- Self-update verifies an ed25519 signature over `SHA256SUMS` when the build embeds trusted release keys; the release workflow signs with `RELEASE_SIGNING_KEY`.

## Capabilities

### Modified Capabilities

- `go-runtime`: dashboard authentication, proxy trust, outbound model-source policy, browser security headers and release integrity.

## Impact

- Code: `internal/application` (auth, bootstrap, limiter, model sources, redaction), `internal/adapters/httpapi`, new `internal/adapters/egress`, `internal/adapters/releases`, `internal/adapters/sqlite` (one migration, schema 34), `cmd/codex-lb` configuration and wiring, new `cmd/release-sign`, one-line marker in `internal/adapters/provider/source_admission.go`, dashboard login/TOTP/session/export/update screens.
- Operators: see `context.md` for upgrade steps (bootstrap token, `--unauthenticated-client`, `--public-origin`, `--allow-private-model-sources`, re-login after upgrade).
- Release: `.github/workflows/release.yml` signs `SHA256SUMS`; trusted public keys are embedded in `internal/adapters/releases/signature.go` (empty until the key is created).
