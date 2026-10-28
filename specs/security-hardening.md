# Security hardening: explicit trust, bounded authentication, revocable sessions and signed updates

- Last edited with skill pack: `0.2.2`

## Title and scope

Remove implicit "local request" trust from dashboard setup and keyless proxy access, and harden dashboard authentication (bounded bodies and hashing, installation-wide failure limits, mandatory configured TOTP, revocable idle-expiring sessions, password re-entry for sensitive actions). Restrict model-source destinations, add browser security headers, and verify ed25519-signed release checksums in the self-updater with a CI signing step.

## Planning anchor

- Entrypoints: `internal/adapters/httpapi/auth.go#passwordSetup`, `#sessionTTL`, `#sessionCookie`, `internal/adapters/httpapi/proxy_auth.go#proxyIngress`, `internal/adapters/httpapi/identity.go#ResolveIdentity`, `internal/adapters/httpapi/server.go#Handler`, `#decodeJSON`, `internal/application/admin_auth.go#Login`, `#State`, `#attempt`, `internal/application/admin_totp.go`, `internal/application/model_sources.go#Create`, `internal/adapters/provider/source_admission.go#beginSource`, `internal/adapters/releases/download.go#Download`, `cmd/codex-lb/config.go#parseConfig`, `deploy/codex-lb.service`, `.github/workflows/release.yml`.
- Changed assumptions:
  - A loopback socket peer with a loopback `Host` is not evidence of a local operator: a same-host reverse proxy (required by the shipped unit, which listens on `127.0.0.1:2455`) makes every internet client look like that. Locality is therefore removed as an authorization input.
  - Authentication endpoints must stay responsive under unauthenticated load, so request bodies, hashing concurrency and failure budgets are bounded independently of the client address.
  - The release checksum file is published from the same origin as the archive; integrity needs a signature by a key that is not stored next to the release.
- Non-local: touches configuration, HTTP adapter, application auth use cases, SQLite schema (one migration), the outbound transport, the release adapter, CI and a few dashboard screens.
- Impacted specs in `specs/`: `specs/engineering-baseline.md` (release workflow R9-R12) — action `reuse`; the signing step extends its release use case without changing its archive contract.
- Impacted OpenSpec capabilities: `go-runtime` — `amend` through change `harden-dashboard-trust-and-update-integrity` (ADDED security requirements, MODIFIED first-run scenario). `admin-auth` and `api-keys` describe the retired runtime and are legacy references (`AGENTS.md`), so they are not amended.

## Connected groups or observed existing logic

1. Entry and orchestration
   - `httpapi.Server.Handler` wraps every route with `nosniff`, `Referrer-Policy`, `X-Frame-Options`, the proxy firewall and `http.CrossOriginProtection`; `/api/*` goes through `requireAdmin`.
   - `cmd/codex-lb/runtime.go#openRuntime` builds `httpapi.Config` and one shared `http.Transport` (`ProxyFromEnvironment`, plain `net.Dialer`) used by ChatGPT, OAuth and model-source traffic; the provider copies the client and disables redirects.
2. Identity and locality
   - `ResolveIdentity` resolves forwarded chains only through trusted hops and sets `Local` when the resolved peer is loopback and `Host` is loopback; `bootstrapTokenRequired`, `proxyIngress` and `sessionTTL` consume `Local`.
   - `--unauthenticated-client` CIDRs are matched against the raw socket peer.
3. Authentication use cases
   - `AdminAuth` keeps one mutex for login, setup, TOTP and password changes, runs bcrypt (cost 12) under it, and limits attempts to 8 per minute per client string.
   - Sessions are Fernet-encrypted JSON (`codex-lb-go-admin-v1`) with nonce, expiry, password tag, TOTP tag and TOTP flag; logout only clears the cookie.
   - `State` requires TOTP only when `runtime_settings.totp_required_on_login` is set; confirming TOTP does not set it.
4. Persistence
   - `admin_secret` (single row) stores the bcrypt hash and encrypted TOTP secret; `InitializeAdminSecret` enables API-key auth when the first password is created.
   - Migrations are an ordered list in `internal/adapters/sqlite/store.go` ending at `schemaV33`.
5. Releases
   - `releases.Source.Download` fetches `SHA256SUMS`, streams the archive, compares SHA-256 and extracts `codex-lb`; redirects are limited to GitHub asset hosts.
6. Frontend consumers
   - `bootstrap-setup-screen.tsx`, `totp-settings.tsx` (requirement toggle), `session-settings.tsx`, `api-keys-section.tsx`, account export and runtime update confirmations.
7. Specs
   - `specs/engineering-baseline.md` owns the release workflow and stays authoritative; this spec adds the signing step only.

## Use cases

### 1. First-run setup always requires a one-time bootstrap token
server start without dashboard password --arm operator token or publish generated token file--> armed bootstrap --setup request with matching token--> [password configured and token consumed, setup refused with 403 or 429]

Requirements:

  R1:
In standard dashboard authentication mode, `POST /api/dashboard-auth/password/setup` SHALL require a bootstrap token for every request regardless of socket peer or `Host`.

  R2:
When `CODEX_LB_DASHBOARD_BOOTSTRAP_TOKEN` is set it SHALL contain 32 to 1024 bytes, otherwise `serve` SHALL refuse to start.

  R3:
When no dashboard password exists and no unconsumed operator token is configured, startup SHALL generate a random token, write it to `<data-dir>/dashboard-bootstrap-token` with mode 0600 and log the file path but never the value.

  R4:
After a successful setup the used token SHALL be rejected: the generated file is removed, and the SHA-256 of an operator token is recorded so the same value is not accepted again after a restart.

  R5:
Removing the dashboard password SHALL publish a new generated token instead of re-accepting a consumed one.

  R6:
Setup attempts SHALL use the authentication failure limits of use case 5 under the `bootstrap` scope.

Files And Functions:
 - planned: internal/application/admin_bootstrap.go - armed token state, verification, consumption, re-arming
 - planned: internal/adapters/credentials/bootstrap_token.go - 0600 token file publisher
 - existing: internal/adapters/httpapi/auth.go - `passwordSetup`, `respondSession` (`bootstrapTokenSource`)
 - planned: internal/adapters/httpapi/startup.go - `Server.Prepare` arms bootstrap and logs configuration warnings
 - existing: cmd/codex-lb/config.go - token length validation, `apiConfig`
 - existing: web/src/features/auth/components/bootstrap-setup-screen.tsx - token-always copy

Tests:
 - description: loopback setup without token is refused
   input: `RemoteAddr=127.0.0.1:x`, `Host=127.0.0.1:2455`, no forwarded headers, no token
   workflow: setup request --passwordSetup--> 403 `invalid_bootstrap_token`
   expected outcome: no password stored
 - description: generated token works once
   input: fresh data dir
   workflow: Prepare --read token file--> setup with token --remove password--> setup with same token
   expected outcome: first setup 200 and file removed; second setup 403; a new file exists
 - description: short operator token is rejected
   input: 31-byte `CODEX_LB_DASHBOARD_BOOTSTRAP_TOKEN`
   workflow: parseConfig --validate--> error
   expected outcome: start refused

### 2. Keyless proxy access only for explicit client networks
proxy request without key while API-key auth is disabled --resolve client through trusted proxies--> [explicit unauthenticated-client match grants local proxy key, otherwise 401]

Requirements:

  R7:
Without a Bearer key, a protected proxy route SHALL use the local proxy principal only when API-key auth is disabled and the resolved client address belongs to a `--unauthenticated-client` CIDR.

  R8:
The resolved client SHALL be the raw peer when no forwarded header is present and the peer is not a trusted proxy, or the trusted-proxy-resolved client when the peer is trusted; a trusted peer without forwarded identity and forwarded headers from an untrusted peer SHALL never qualify.

  R9:
`serve` SHALL refuse `--unauthenticated-client` CIDRs that overlap a `--trusted-proxy` CIDR and SHALL log a warning when an unauthenticated-client CIDR contains loopback addresses.

  R10:
The dashboard SHALL display a persistent warning while API-key authentication is disabled.

Files And Functions:
 - existing: internal/adapters/httpapi/proxy_auth.go - `proxyIngress`, `keylessClient`
 - existing: internal/adapters/httpapi/identity.go - remove `Identity.Local` and `localHost`
 - existing: cmd/codex-lb/config.go - overlap validation
 - planned: web/src/features/api-keys/components/api-key-auth-warning.tsx - warning rendered by `web/src/App.tsx#AppLayout` on every page

Tests:
 - description: loopback without flag requires a key
   input: `api_key_auth_enabled=0`, loopback peer and Host, no CIDR
   workflow: GET /v1/models --proxyIngress--> 401
   expected outcome: 401 `invalid_api_key`
 - description: forwarded client inside CIDR through trusted proxy
   input: trusted peer 127.0.0.1, XFF 10.1.2.3, CIDR 10.0.0.0/8
   workflow: GET /v1/models --resolve--> keyless access
   expected outcome: 200; with untrusted peer or XFF outside the CIDR 401

### 3. Operators can see and fix proxy trust
request with forwarded headers from an untrusted loopback peer --warn once--> logged configuration warning; request with `--public-origin https://…` --issue cookie--> Secure `__Host-` cookie and HSTS

Requirements:

  R11:
The first request that carries forwarded client headers from an untrusted loopback peer SHALL produce one WARN log naming the peer and the `--trusted-proxy` remedy.

  R12:
`--public-origin` SHALL accept only an `https://host[:port]` origin; when set, dashboard cookies SHALL be `Secure`, the admin cookie SHALL be named `__Host-codex_lb_dashboard_session`, responses SHALL carry `Strict-Transport-Security` and the origin SHALL be trusted by cross-origin protection.

  R13:
The shipped systemd unit SHALL pass `--trusted-proxy 127.0.0.1/32 --trusted-proxy ::1/128`.

Files And Functions:
 - existing: internal/adapters/httpapi/server.go - `Handler` warning and headers
 - existing: internal/adapters/httpapi/auth.go - `secureRequest`, cookie names
 - existing: deploy/codex-lb.service - ExecStart

Tests:
 - description: untrusted forwarding warning
   input: loopback peer, no trusted proxies, XFF present
   workflow: two requests --Handler--> one WARN record
   expected outcome: exactly one warning
 - description: secure cookies by configuration
   input: `PublicOrigin=https://lb.example`
   workflow: setup --cookie--> Set-Cookie
   expected outcome: `__Host-` name, `Secure`, `SameSite=Strict`, HSTS header

### 4. Authentication endpoints bound bodies and hashing
unauthenticated auth request --limit body to 8 KiB--> [413, decoded request] --acquire hashing slot--> [429 `authentication_busy`, bcrypt outside the state mutex]

Requirements:

  R14:
Requests to `/api/dashboard-auth/*` and `/api/key-reports/session` SHALL be limited to 8 KiB of body and answer 413 `request_too_large` above it.

  R15:
bcrypt comparisons and hashing SHALL run outside the authentication state mutex and at most four at a time; a request that finds no free slot SHALL receive 429 with `Retry-After: 1` immediately.

Files And Functions:
 - existing: internal/adapters/httpapi/server.go - `decodeJSON` 413 mapping, `limitBody`
 - existing: internal/application/admin_auth.go - hashing semaphore, lock scope

Tests:
 - description: oversized login body
   input: 9 KiB JSON body
   workflow: POST login --limitBody--> 413
   expected outcome: no bcrypt work
 - description: saturated hashing does not block session reads
   input: four blocked hash slots
   workflow: login --acquire--> 429; GET session --> 200
   expected outcome: Retry-After 1

### 5. Authentication failures are limited per client and per installation
auth attempt --check installation lock and per-client window--> [429 with Retry-After, verification] --record outcome--> updated counters

Requirements:

  R16:
Per scope (`password`, `totp`, `bootstrap`) the system SHALL allow 8 attempts per minute per client, where IPv6 clients are grouped by /64 and IPv4 clients by address.

  R17:
Per scope the system SHALL count failures installation-wide; after 10 failures without a 15-minute quiet period every further attempt from any client SHALL be refused for `min(2^(n-10)` seconds, 15 minutes`)` after the last failure, answered as 429 with `Retry-After`.

  R18:
A success SHALL reset only the per-client window; rejected attempts SHALL be logged at WARN with scope, reason and client address and without submitted secrets.

Files And Functions:
 - planned: internal/application/admin_auth_limiter.go - `authLimiter`
 - existing: internal/application/admin_auth.go, internal/application/admin_totp.go - scopes
 - existing: internal/adapters/httpapi/auth.go - `authFail` logging

Tests:
 - description: installation-wide lock across clients
   input: 11 wrong passwords from 11 different IPv4 clients
   workflow: login ×11 --limiter--> 12th attempt from a new client
   expected outcome: 429 with positive Retry-After even with the correct password
 - description: IPv6 /64 grouping
   input: nine attempts from addresses in one /64
   workflow: login ×9
   expected outcome: ninth attempt 429

### 6. Configured TOTP is required at every sign-in
password login --check TOTP configured--> [full session without TOTP, 5-minute pending session] --verify code--> full session

Requirements:

  R19:
When a TOTP secret is configured, a session SHALL be fully authenticated only after TOTP verification, independent of `totp_required_on_login`; the settings API SHALL report `totpRequiredOnLogin` as the configured state and SHALL ignore attempts to change it.

  R20:
The TOTP settings screen SHALL state that TOTP is required at every sign-in while configured instead of offering a toggle.

Files And Functions:
 - existing: internal/application/admin_auth.go - `State`, `authorizedSecret`, `Login`
 - existing: internal/adapters/httpapi/admin.go - `getSettings`, `saveSettings`
 - existing: web/src/features/settings/components/totp-settings.tsx

Tests:
 - description: password-only session after TOTP confirm
   input: TOTP confirmed, new password login without code
   workflow: login --> GET /api/settings
   expected outcome: 401 until `/totp/verify` succeeds

### 7. Dashboard sessions are revocable and expire when idle
issued session --request within idle window--> refreshed idle deadline --logout--> revoked session id; issued sessions --logout everywhere--> advanced epoch

Requirements:

  R21:
Session tokens SHALL carry a session id, the installation session epoch, an absolute expiry and an idle deadline; tokens of the previous format SHALL be rejected.

  R22:
`POST /api/dashboard-auth/logout` SHALL revoke the presented session until its absolute expiry; `POST /api/dashboard-auth/logout-all` SHALL require a full session and advance the epoch so every previously issued session is rejected.

  R23:
A session unused for 12 hours SHALL be rejected; authenticated requests SHALL extend the idle deadline (at most every 10 minutes) without extending the absolute expiry.

  R24:
The effective absolute lifetime SHALL be the configured `dashboard_session_ttl` (default 24 hours) capped at 30 days for every client; password-only sessions awaiting TOTP stay capped at 5 minutes.

Tables:
admin_secret
 - session_epoch: INTEGER NOT NULL DEFAULT 0 # advanced only by logout-all
dashboard_session_revocations
 - session_hash: TEXT PRIMARY KEY # SHA-256 of the session id
 - expires_at: INTEGER NOT NULL # pruned after the token's absolute expiry
dashboard_bootstrap_consumed
 - token_hash: TEXT PRIMARY KEY # SHA-256 of a consumed operator token
 - consumed_at: INTEGER NOT NULL

Files And Functions:
 - existing: internal/application/admin_auth.go - token v2, `Authorize` refresh, `Logout`, `LogoutAll`
 - planned: internal/adapters/sqlite/dashboard_sessions.go - revocation, epoch, consumed tokens
 - existing: internal/adapters/sqlite/migrations.go, store.go - one migration
 - existing: web/src/features/settings/components/session-settings.tsx - sign out everywhere

Tests:
 - description: logout revokes a copied token
   input: session cookie copied before logout
   workflow: logout --> GET /api/settings with copied cookie
   expected outcome: 401
 - description: logout everywhere
   input: two sessions
   workflow: logout-all from one --> both used
   expected outcome: both 401
 - description: idle expiry
   input: clock advanced 12 h 1 min without requests
   workflow: GET /api/settings
   expected outcome: 401

### 8. Model-source destinations are public unless explicitly allowed
create or update model source --validate base URL host--> [422 for literal private host, stored source] --dial upstream--> [refused private resolution, connected public address]

Requirements:

  R25:
Without `--allow-private-model-sources`, creating or changing a model-source base URL to a literal loopback, private, link-local, CGNAT, multicast, unspecified or reserved address, or to `localhost`, SHALL return 422.

  R26:
Without that flag, every model-source connection SHALL resolve the destination and dial only addresses that are public unicast; a name resolving to any non-public address SHALL be refused before connecting, so DNS changes cannot redirect a stored source. Redirect responses from sources SHALL NOT be followed.

  R27:
When an environment HTTP proxy is used, the destination name SHALL be checked before the request is handed to the proxy and the proxy itself SHALL remain dialable.

Files And Functions:
 - planned: internal/domain/egress.go - `PublicUnicast`
 - planned: internal/adapters/egress/guard.go - `Restrict`, `Guard.DialContext`, `Guard.Proxy`
 - existing: internal/adapters/provider/source_admission.go - mark source contexts restricted (one line)
 - existing: internal/application/model_sources.go - destination validation
 - existing: cmd/codex-lb/runtime.go - wrap transport dial and proxy functions

Tests:
 - description: literal metadata address rejected
   input: base URL `http://169.254.169.254/v1`
   workflow: POST /api/model-sources --> 422
   expected outcome: not stored
 - description: rebinding name refused at dial
   input: resolver returning 127.0.0.1 for a public-looking name
   workflow: guarded request --DialContext--> error
   expected outcome: no connection to the loopback listener

### 9. Browser responses carry CSP and sensitive actions require the password
dashboard response --set headers--> CSP (+ HSTS in secure mode); export auth or runtime apply/rollback --verify current password--> [403 `reauthentication_required`, action performed]

Requirements:

  R28:
Dashboard and admin API responses SHALL carry a Content-Security-Policy limited to same-origin scripts, styles (inline styles allowed), images including `data:`, and same-origin connections, with `object-src 'none'`, `base-uri 'none'` and `frame-ancestors 'none'`; proxy routes SHALL carry `default-src 'none'; sandbox`.

  R29:
In standard mode with a configured password, `POST /api/accounts/{id}/export/auth`, `POST /api/runtime/updates/apply` and `POST /api/runtime/updates/rollback` SHALL require `currentPassword`, verified under the `password` failure scope; a missing or wrong value SHALL return 403 `reauthentication_required` without performing the action.

  R30:
`--dashboard-auth-mode=disabled` SHALL be accepted only with a loopback `--listen` address and SHALL log a startup warning.

  R31:
Error-archive redaction SHALL also mask AWS access keys, PEM private keys, Google API keys, GitHub and Slack tokens and URL credentials.

Files And Functions:
 - existing: internal/adapters/httpapi/server.go - CSP and HSTS
 - existing: internal/adapters/httpapi/accounts.go, runtime_updates.go - step-up
 - existing: internal/application/admin_auth.go - `Reauthenticate`
 - existing: internal/application/error_archive_redaction.go
 - existing: web/src/features/accounts, web/src/features/runtime - password prompts

Tests:
 - description: export without password
   input: full session, no `currentPassword`
   workflow: POST export --> 403
   expected outcome: no tokens in the response

### 10. Self-update verifies signed checksums
update download --fetch SHA256SUMS and SHA256SUMS.sig--> [embedded trusted keys present: verify ed25519 signature or refuse, no embedded keys: checksum-only transition mode] --verify archive digest--> extracted binary

Requirements:

  R32:
When the binary embeds at least one trusted release public key, `Download` SHALL fetch `SHA256SUMS.sig` and SHALL refuse the update unless the base64 ed25519 signature over the exact `SHA256SUMS` bytes verifies with one of the embedded keys; this check SHALL complete before the archive is downloaded or extracted.

  R33:
When no trusted key is embedded, `Download` SHALL keep checksum-only verification so releases published before signing remain installable.

  R34:
The release workflow SHALL sign `SHA256SUMS` with the PKCS#8 PEM ed25519 key from the `RELEASE_SIGNING_KEY` secret using `cmd/release-sign`, upload `SHA256SUMS.sig`, and fail when the repository embeds trusted keys but the secret is absent or does not match one of them.

Files And Functions:
 - planned: internal/adapters/releases/signature.go - trusted key list placeholder and verification
 - existing: internal/adapters/releases/download.go - signature check before archive download
 - planned: cmd/release-sign/main.go - `sign` and `public-key` commands
 - existing: .github/workflows/release.yml - signing step and asset upload

Tests:
 - description: signature matrix
   input: valid signature, missing `.sig`, foreign key, modified `SHA256SUMS`
   workflow: Download --verify--> result
   expected outcome: only the valid signature extracts a binary; failures leave the staging directory empty and never fetch the archive

## Implementation checklist

1. [x] Config: token length, CIDR overlap, `--public-origin`, `--allow-private-model-sources`, disabled-mode listen check, `apiConfig` (R2, R9, R12, R25, R30).
2. [x] Remove locality; keyless access through explicit CIDRs; untrusted forwarding warning; unit ExecStart (R7, R8, R11, R13).
3. [x] Bootstrap arming, file publisher, consumption, re-arming; `Server.Prepare` (R1, R3-R6).
4. [x] Body limits, hashing semaphore, limiter with installation scope and /64 grouping, rejection logging (R14-R18).
5. [x] TOTP always required when configured; settings API derivation (R19).
6. [x] Migration, session v2 with epoch, revocation, idle refresh, logout-all, TTL cap (R21-R24).
7. [x] Egress guard, provider marker, model-source validation (R25-R27).
8. [x] CSP/HSTS/`__Host-`/SameSite=Strict, step-up endpoints, redaction patterns (R12, R28, R29, R31).
9. [x] Release signature verification, `cmd/release-sign`, workflow step (R32-R34).
10. [x] Dashboard: bootstrap copy, TOTP info row, disabled-auth warning, sign out everywhere, password prompts (R10, R20, R22, R29).
11. [x] OpenSpec change, `go vet`, `golangci-lint`, `go test`, race tests, web lint/typecheck/tests.

## Open questions

None blocking. The administrator generates the release signing key, embeds its public key and configures `RELEASE_SIGNING_KEY` (use case 10).

## Decision log

- 2026-10-28: Locality is removed instead of narrowed. Any loopback heuristic remains wrong behind a same-host proxy, and an explicit `--unauthenticated-client` CIDR already expresses the dev use case.
- 2026-10-28: TOTP is derived from "configured" rather than set atomically at confirm, so a stored flag cannot drift from the secret; the settings field stays for API compatibility as a read-only mirror.
- 2026-10-28: Per-session logout uses a revocation list keyed by session id; logout everywhere uses an epoch stored in `admin_secret`, which resets together with the password tag when the password is removed.
- 2026-10-28: The installation-wide limiter can delay a legitimate administrator during an attack; the delay is capped at 15 minutes, existing sessions keep working, and success does not reset the shared counter so attackers cannot interleave it away.
- 2026-10-28: The SSRF guard is attached to the shared transport but enforced only for contexts marked by the provider's model-source admission, so ChatGPT and OAuth traffic keep their existing path.
- 2026-10-28: Releases built without embedded keys keep checksum-only updates; the first release that embeds a key is installed by older binaries as before, and every later update requires a valid signature.
- 2026-10-28: Deferred: per-key memory budgets (SEC-4, transport owner), connection limit (SEC-19, capacity owner), opt-in error-archive bodies (SEC-20 storage part), `trusted_header` allowlist and audit table (SEC-14/SEC-10 remainder), key separation (SEC-18).
- 2026-10-28: The branch is cut from `chore/engineering-baseline` because the signing step extends its `.github/workflows/release.yml` and the lint gate uses its `.golangci.yml`; it must merge after that branch.
- 2026-10-28: Password re-entry failures answer 403 `reauthentication_required` rather than 401 so the dashboard's unauthorized handler does not end a valid session on a mistyped password.
- 2026-10-28: Model-source egress is enforced by marking the context in `provider.beginSource` (the single admission point of all four source protocols) and wrapping the runtime transport's dial and proxy functions; this is the only edit in the provider and runtime wiring zones besides passing `cfg.apiConfig` and calling `Server.Prepare`.
- 2026-10-28: Verification: `go vet ./...`, `golangci-lint run ./...` (0 issues), `go test -count=1 ./...`, race tests of touched packages, dashboard lint/typecheck/vitest, strict OpenSpec validation.
