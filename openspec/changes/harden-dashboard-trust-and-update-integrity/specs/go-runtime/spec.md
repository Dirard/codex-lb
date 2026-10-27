## ADDED Requirements

### Requirement: First-run dashboard setup always requires a one-time bootstrap token

In standard dashboard authentication mode, password setup SHALL require a bootstrap token for every request, independent of socket peer, `Host` and forwarded headers. `CODEX_LB_DASHBOARD_BOOTSTRAP_TOKEN`, when set, MUST contain 32 to 1024 printable characters without spaces or `serve` SHALL refuse to start. When no dashboard password exists and no unconsumed operator token is configured, startup SHALL write a random token to `<data-dir>/dashboard-bootstrap-token` with mode 0600 and log the file path but never the token. A successful setup SHALL invalidate the token used: the generated file is removed and the SHA-256 of an operator token is persisted so that value is refused after restarts. Removing the dashboard password SHALL publish a new generated token. Setup attempts SHALL count against the `bootstrap` authentication failure scope. The session response SHALL report whether a token is required and whether it comes from the environment or the generated file.

#### Scenario: Loopback setup without a token is refused
- **WHEN** a setup request arrives from `127.0.0.1` with `Host: 127.0.0.1:2455` and no forwarded headers but without a bootstrap token
- **THEN** the response is 403 `invalid_bootstrap_token` and no password is stored

#### Scenario: A generated token works exactly once
- **WHEN** the server starts without a password and without an operator token
- **THEN** a 0600 token file exists in the data directory, its path is logged and its value is not
- **AND** setup with that token succeeds and removes the file
- **AND** after the password is removed a different token is published and the previous one is refused

#### Scenario: A short operator token is rejected at start
- **WHEN** `CODEX_LB_DASHBOARD_BOOTSTRAP_TOKEN` has fewer than 32 characters
- **THEN** `serve` exits with a configuration error that does not echo the token

### Requirement: Keyless proxy access requires an explicit client network

While API-key authentication is disabled, a protected proxy route without a Bearer key SHALL use the local proxy principal only when the resolved client address belongs to a configured `--unauthenticated-client` network. The resolved client SHALL be the raw peer when no forwarded header is present and the peer is not a trusted proxy, or the trusted-proxy-resolved client when the peer is a trusted proxy. A trusted proxy without forwarded client identity, and forwarded headers from an untrusted peer, MUST NOT qualify. Socket or `Host` locality MUST NOT grant keyless access. `serve` SHALL refuse unauthenticated-client networks that overlap a trusted-proxy network and SHALL log a warning when such a network contains loopback addresses. Required-capability requests, usage self-service and Realtime SHALL keep requiring a real key. The dashboard SHALL display a warning on every page while API-key authentication is disabled.

#### Scenario: Loopback without an explicit network requires a key
- **WHEN** API-key authentication is disabled and a loopback client calls `/v1/models` without a key and no unauthenticated-client network is configured
- **THEN** the response is 401

#### Scenario: Proxied client inside the network
- **WHEN** a trusted proxy at `127.0.0.1` forwards client `10.1.2.3` and `10.0.0.0/8` is an unauthenticated-client network
- **THEN** the request proceeds without a key
- **AND** the same request without `X-Forwarded-For`, or with a forwarded client outside the network, or from an untrusted peer, is rejected with 401

### Requirement: Proxy trust and secure cookies are explicit operator configuration

The first request carrying forwarded client headers from an untrusted loopback peer SHALL produce one WARN log naming the peer and the `--trusted-proxy` remedy. `--public-origin` SHALL accept only an `https://host[:port]` origin without path, query or credentials. When it is set, or the connection uses TLS, dashboard cookies SHALL be `Secure`, the administrator cookie SHALL be named `__Host-codex_lb_dashboard_session` with `Path=/` and no `Domain`, and responses SHALL include `Strict-Transport-Security`. The public origin SHALL be trusted by cross-origin protection. Dashboard and report cookies SHALL use `SameSite=Strict`. The shipped systemd unit SHALL pass `--trusted-proxy 127.0.0.1/32 --trusted-proxy ::1/128`.

#### Scenario: Secure mode cookie
- **WHEN** the server runs with `--public-origin https://lb.example` and an administrator completes setup over plain HTTP from the proxy
- **THEN** the cookie is `__Host-codex_lb_dashboard_session`, `Secure`, `HttpOnly`, `SameSite=Strict`, and the response carries HSTS

#### Scenario: Untrusted forwarding is reported once
- **WHEN** two requests with `X-Forwarded-For` arrive from a loopback peer that is not a trusted proxy
- **THEN** exactly one warning is logged

### Requirement: Authentication endpoints bound request bodies and password hashing

Requests to `/api/dashboard-auth/*` and `/api/key-reports/session` SHALL be limited to 8 KiB of body, including bodies without a declared length, and SHALL answer 413 `request_too_large` above it. Password hashing and comparison SHALL run outside the authentication state lock with at most four concurrent operations; a request that finds no free slot SHALL immediately receive 429 `authentication_busy` with `Retry-After: 1` and MUST NOT spend its attempt budget.

#### Scenario: Oversized login body
- **WHEN** a login request carries a 9 KiB body
- **THEN** the response is 413 and no password hash is computed

#### Scenario: Saturated hashing fails fast
- **WHEN** all hashing slots are busy
- **THEN** a login receives 429 with `Retry-After: 1` and session state reads still succeed

### Requirement: Authentication failures are limited per client and per installation

For each scope (`password`, `totp`, `bootstrap`) the system SHALL allow 8 attempts per minute per client, grouping IPv6 clients by /64 and IPv4 clients by address. The system SHALL also count failures per scope for the whole installation: after 10 failures without a 15-minute quiet period, attempts from every client SHALL be refused until `min(2^(n−10) seconds, 15 minutes)` after the last failure, where `n` is the failure count, answered as 429 `login_rate_limited` with `Retry-After`. A success SHALL reset only the caller's per-client window. Rejected attempts SHALL be logged at WARN with scope reason and client address and MUST NOT log submitted secrets.

#### Scenario: Failures from many addresses lock the installation
- **WHEN** 10 wrong passwords arrive from 10 different client addresses
- **THEN** the correct password from an eleventh address receives 429 with `Retry-After: 1`
- **AND** one more failure after that delay raises the next `Retry-After` to 2

#### Scenario: IPv6 prefix grouping
- **WHEN** 8 attempts arrive from different addresses in one IPv6 /64
- **THEN** the next attempt from that /64 is refused while another /64 is not

### Requirement: A configured TOTP is required at every sign-in

When a TOTP secret is configured, a dashboard session SHALL be fully authenticated only after TOTP verification, regardless of `totp_required_on_login`. Password-only sessions SHALL expire after at most 5 minutes. The settings API SHALL report `totpRequiredOnLogin` as the configured state and SHALL ignore attempts to change it. The TOTP settings screen SHALL state that a code is required at every sign-in while TOTP is configured and SHALL NOT offer a toggle.

#### Scenario: Password-only session after TOTP setup
- **WHEN** TOTP was confirmed and an administrator signs in with the password only
- **THEN** `/api/settings` answers 401 until `/api/dashboard-auth/totp/verify` succeeds
- **AND** `GET /api/settings` reports `totpRequiredOnLogin: true` even after a settings update sends `false`

### Requirement: Dashboard sessions are revocable and expire when idle

Dashboard session tokens SHALL carry a random session id, the installation session epoch, an absolute expiry and an idle deadline; tokens of the earlier format SHALL be rejected. `POST /api/dashboard-auth/logout` SHALL revoke the presented session until its absolute expiry. `POST /api/dashboard-auth/logout-all` SHALL require a fully authenticated session and SHALL advance the epoch so every previously issued session is rejected. A session without an authenticated request for 12 hours SHALL be rejected; authenticated requests SHALL extend the idle deadline at most every 10 minutes without extending the absolute expiry. The absolute lifetime SHALL be the configured dashboard session lifetime (default 24 hours) capped at 30 days for every client. The dashboard SHALL offer "sign out everywhere" in its session settings.

#### Scenario: Logout revokes a copied cookie
- **WHEN** a session cookie is copied and the session then logs out
- **THEN** the copied cookie receives 401 on `/api/settings`

#### Scenario: Sign out everywhere
- **WHEN** two sessions exist and one calls `logout-all`
- **THEN** both receive 401 afterwards

#### Scenario: Idle session
- **WHEN** 12 hours pass without an authenticated request
- **THEN** the session is rejected although its absolute expiry has not passed

### Requirement: Model-source destinations are public unless explicitly allowed

Without `--allow-private-model-sources`, creating a model source or changing its base URL to `localhost`, a `.localhost` name, or a literal loopback, private, link-local, carrier-grade NAT, multicast, unspecified, reserved or IPv4-embedding IPv6 address SHALL return 422. Without that flag every model-source connection SHALL resolve its destination and dial only checked public unicast addresses; a name resolving to any non-public address SHALL be refused before a connection is made. When an environment HTTP proxy is used, the destination name SHALL be checked before the request is handed to the proxy and the proxy endpoint SHALL remain dialable. Redirect responses from model sources SHALL NOT be followed. Subscription, OAuth and release traffic SHALL keep its existing path.

#### Scenario: Metadata address rejected at creation
- **WHEN** an administrator creates a source with base URL `http://169.254.169.254/latest`
- **THEN** the response is 422 and nothing is stored

#### Scenario: Name resolving to loopback refused at dial
- **WHEN** a stored source's host name resolves to `127.0.0.1`
- **THEN** the request fails before any connection to the loopback address

#### Scenario: Self-hosted source with the opt-in
- **WHEN** `--allow-private-model-sources` is set
- **THEN** a source at `http://127.0.0.1:11434/v1` can be created and used

### Requirement: Dashboard responses carry a content security policy

Dashboard and admin API responses SHALL include a Content-Security-Policy that allows scripts, connections, fonts and styles only from the same origin (inline styles allowed), images from the same origin plus `data:` and `blob:`, and sets `object-src 'none'`, `base-uri 'none'`, `form-action 'self'` and `frame-ancestors 'none'`. Proxy routes SHALL carry `default-src 'none'; frame-ancestors 'none'; sandbox`.

#### Scenario: Policy headers
- **WHEN** a browser loads `/` or `/api/settings`
- **THEN** the response carries the dashboard policy
- **AND** `/v1/models` carries the sandbox policy

### Requirement: Sensitive administrator actions require the current password

In standard dashboard authentication mode, `POST /api/accounts/{id}/export/auth`, `POST /api/runtime/updates/apply` and `POST /api/runtime/updates/rollback` SHALL require `currentPassword` in the JSON body. The password SHALL be verified under the `password` failure scope; a missing or wrong value SHALL return 403 `reauthentication_required` without performing the action and without ending the session. Trusted-header and disabled modes SHALL NOT require it. The dashboard SHALL prompt for the password before these actions.

#### Scenario: Export without the password
- **WHEN** a fully authenticated administrator exports account tokens without `currentPassword`
- **THEN** the response is 403 `reauthentication_required` and contains no tokens

### Requirement: Disabled dashboard authentication is limited to loopback listeners

`--dashboard-auth-mode=disabled` SHALL be accepted only with a loopback `--listen` address and SHALL log a startup warning that every client reaching the listener is an administrator.

#### Scenario: Disabled mode on all interfaces
- **WHEN** `serve` is started with `--dashboard-auth-mode disabled --listen 0.0.0.0:2455`
- **THEN** it exits with a configuration error

### Requirement: Error archives redact common credential formats

Error-archive redaction SHALL mask, in addition to bearer values, OpenAI-style keys and JWTs, AWS access key IDs, Google API keys, GitHub and Slack tokens, PEM private-key blocks and URL user-info credentials, while preserving surrounding text.

#### Scenario: Free-text credentials
- **WHEN** an archived diagnostic message contains an AWS access key ID and `https://user:secret@db.example/x`
- **THEN** the stored message contains `[redacted]` in their place and keeps `https://[redacted]@db.example/x`

### Requirement: Self-update verifies signed release checksums

When the running binary embeds at least one trusted ed25519 release public key, the updater SHALL download `SHA256SUMS.sig` and SHALL refuse the update unless the base64 signature over the exact `SHA256SUMS` bytes verifies with an embedded key; this check SHALL complete before the archive is downloaded or extracted, and a refused update SHALL leave the staging directory empty. When no key is embedded, the updater SHALL keep checksum-only verification so earlier releases remain installable. The release workflow SHALL sign `SHA256SUMS` with the PKCS#8 PEM ed25519 key from the `RELEASE_SIGNING_KEY` secret, publish `SHA256SUMS.sig`, and fail when the build embeds trusted keys but the secret is missing or does not match an embedded key. Key rotation SHALL be possible by embedding several keys.

#### Scenario: Signature matrix
- **WHEN** a build embeds a trusted key and a release offers a valid signature, no signature, a signature by another key, or a modified `SHA256SUMS`
- **THEN** only the valid signature produces an extracted binary and no other case downloads the archive

#### Scenario: Transition release
- **WHEN** a build embeds no key
- **THEN** a release with only `SHA256SUMS` is installed after its checksum matches and no signature is requested

## MODIFIED Requirements

### Requirement: Keep domain rules independent of transport and storage

Business rules SHALL be independent of HTTP, SQLite and provider SDK implementations. Application scenarios SHALL own their required ports; the composition root SHALL wire concrete adapters. The implementation MUST NOT introduce a generic framework or duplicate provider behavior merely to satisfy layer naming. Request-owned resources and background jobs SHALL have explicit bounded lifetimes.

#### Scenario: Exercise routing without real upstream services
- **WHEN** tests supply deterministic clocks, repository state and upstream adapters
- **THEN** ownership, quota eligibility and failover rules can be checked without provider credentials or production requests

#### Scenario: First-run dashboard distinguishes local and remote setup
- **WHEN** the dashboard loads before a password is configured
- **THEN** no request is classified as local: its session response requires a bootstrap token for every standard-mode request and names whether the token comes from the environment or the generated file in the data directory
- **AND** the dashboard remains gated until setup completes, and no socket peer, `Host` value or forwarded header bypasses bootstrap verification
