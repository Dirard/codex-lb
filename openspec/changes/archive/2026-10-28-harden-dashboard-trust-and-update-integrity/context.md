# Context: explicit trust, bounded authentication and signed updates

## Purpose

The service is published through a reverse proxy on the same host (the unit listens on `127.0.0.1:2455`). Behind such a proxy every client reaches the process from a loopback socket, so "loopback peer and loopback `Host`" cannot identify the operator. This change removes locality as an authorization input and makes every trust decision explicit: a bootstrap token for first-run setup, an operator-listed network for keyless proxy access, a configured public origin for secure cookies, and embedded release keys for updates.

## Decisions

- **No locality.** A request is never "local". `Identity` keeps only the resolved client address, whether the peer is a trusted proxy and whether forwarded headers were present. Socket/Host heuristics stay only in display code (connect-address help).
- **Bootstrap tokens are one-time.** An operator token (`CODEX_LB_DASHBOARD_BOOTSTRAP_TOKEN`, 32–1024 printable characters) is consumed by the first successful setup; its SHA-256 is stored in `dashboard_bootstrap_consumed`, so the same value is refused after a restart. Without an unconsumed operator token the server writes a random token to `<data-dir>/dashboard-bootstrap-token` (0600, atomically replaced) and logs the path, never the value. A successful setup deletes the file; removing the password publishes a new one.
- **Keyless access is a listed network.** With API-key auth disabled, `/v1`, `/backend-api` and `/api/codex` accept a request without a key only when the resolved client belongs to `--unauthenticated-client`. The client is the raw peer for direct connections and the trusted-proxy-resolved address for proxied ones. A trusted proxy that sends no client identity, and forwarded headers from an untrusted peer, never qualify. `serve` refuses unauthenticated-client networks overlapping `--trusted-proxy`.
- **Failure limits per client and per installation.** Each scope (`password`, `totp`, `bootstrap`) allows 8 attempts per minute per client (IPv6 grouped by /64). Installation-wide, 10 failures without a 15-minute quiet period start an exponential delay (1 s, 2 s, 4 s … capped at 15 minutes) for every client, answered with 429 and `Retry-After`. Success clears only the caller's window so the shared counter cannot be reset by interleaving. Existing sessions are unaffected by the delay.
- **Hashing is bounded.** bcrypt (cost 12) runs outside the state mutex; at most four hashes run at once and further requests get 429 `authentication_busy` with `Retry-After: 1` instead of queueing.
- **TOTP is derived from the secret.** A configured TOTP secret always requires a code at sign-in. `totpRequiredOnLogin` in the settings API mirrors that state and writes to it are ignored, so a stored flag cannot drift from the secret.
- **Revocable sessions.** Session tokens (`codex-lb-go-admin-v2`) contain a random id, the installation epoch, an idle deadline and an absolute expiry. Logout stores the id digest until expiry (`dashboard_session_revocations`); "sign out everywhere" advances `admin_secret.session_epoch`. Authenticated requests extend the idle deadline at most every 10 minutes; 12 idle hours end a session. The absolute lifetime is the configured value (default 24 h) capped at 30 days for every client. Password-only sessions awaiting TOTP stay at 5 minutes.
- **Model-source egress.** The provider marks every model-source request context; the shared transport's dial guard then resolves the destination itself and dials only checked public unicast addresses (loopback, private, link-local, CGNAT, multicast, reserved and IPv4-embedding IPv6 forms are refused; a mixed DNS answer is refused as a whole). Behind an environment HTTP proxy the destination name is checked before the request is handed to the proxy, and the operator's proxy endpoint stays dialable. Subscription, OAuth and release traffic is unmarked and unchanged. Model-source redirects are never followed (existing provider client policy).
- **Browser policy.** Dashboard and admin responses carry `default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'`. Proxy routes carry `default-src 'none'; frame-ancestors 'none'; sandbox`. Inline styles stay allowed because UI components set style attributes. With `--public-origin` (or direct TLS) responses send `Strict-Transport-Security: max-age=31536000` and the admin cookie is `__Host-codex_lb_dashboard_session` (`Secure`, `Path=/`, no `Domain`). Both cookies use `SameSite=Strict`; the dashboard is a same-origin SPA, so top-level navigation does not need the cookie.
- **Password re-entry.** In standard mode `POST /api/accounts/{id}/export/auth` and `POST /api/runtime/updates/{apply,rollback}` require `currentPassword`; a missing or wrong value returns 403 `reauthentication_required` (not 401, so the dashboard keeps its session) and counts against the `password` failure scope. Trusted-header and disabled modes authenticate administrators elsewhere and skip the prompt.
- **Release signatures.** `SHA256SUMS.sig` is the base64 ed25519 signature over the exact `SHA256SUMS` bytes. `internal/adapters/releases/signature.go` embeds a list of trusted public keys (rotation: ship a build with old and new keys, sign with the new key, later drop the old one). The signature is verified before the archive is downloaded or extracted.

## Transition for unsigned releases

- While the embedded key list is empty (this change ships it empty), updates are verified by SHA-256 only — exactly as before — so existing releases stay installable.
- The first release whose binary embeds a key is still installed by older binaries using checksums only (they do not know about signatures). From that binary on, every update must carry a valid signature by an embedded key; an unsigned or foreign-signed release is refused before download.
- `cmd/release-sign sign` succeeds without `RELEASE_SIGNING_KEY` only while no key is embedded (it logs that the release is unsigned). Once a key is embedded, a missing secret or a secret that does not match an embedded key fails the release workflow.

## Administrator steps for release signing

1. Generate the key offline: `openssl genpkey -algorithm ed25519 -out codex-lb-release.pem` (keep it off the repository and off the VPS).
2. Print the public key: `RELEASE_SIGNING_KEY="$(cat codex-lb-release.pem)" go run ./cmd/release-sign public-key`.
3. Add that base64 value to `trustedReleaseKeys` in `internal/adapters/releases/signature.go` and release it.
4. Store the PEM content as the GitHub Actions secret `RELEASE_SIGNING_KEY` before pushing the next tag. Verify a published release locally with `go run ./cmd/release-sign verify SHA256SUMS` next to the downloaded `SHA256SUMS.sig`.

## Operator migration notes

- **Re-login once.** Earlier session cookies are rejected after the upgrade (new token format); report-key cookies keep working.
- **First-run or after password removal.** Read the token from the logged path, e.g. `sudo cat /var/lib/codex-lb/dashboard-bootstrap-token` (Docker: `docker exec <container> cat /data/dashboard-bootstrap-token`), or set `CODEX_LB_DASHBOARD_BOOTSTRAP_TOKEN` to at least 32 characters. Tokens shorter than 32 characters now stop `serve` at start.
- **Local keyless development.** Installations that relied on keyless loopback access with API-key auth disabled now receive 401. Either create an API key, or start with `--unauthenticated-client 127.0.0.1/32 --unauthenticated-client ::1/128` (a warning reminds that a same-host proxy must then be listed with `--trusted-proxy`, and the two lists must not overlap).
- **Reverse proxy.** The unit now passes `--trusted-proxy 127.0.0.1/32 --trusted-proxy ::1/128`; the proxy must send `X-Forwarded-For` (for nginx `proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;` and `proxy_set_header X-Forwarded-Proto $scheme;`). Add `--public-origin https://lb.example.com` so cookies are `Secure` regardless of proxy headers.
- **Self-hosted model sources** on loopback or private networks need `--allow-private-model-sources`; without it such sources fail with `upstream_unavailable` and a one-time WARN names the host.
- **TOTP.** If TOTP was configured but not required, sign-in now asks for a code.
- **Session lifetime.** Values above 30 days act as 30 days; a 1-year legacy value is no longer honored for loopback clients.
- **`--dashboard-auth-mode=disabled`** now requires a loopback `--listen` address.

## Example

```text
$ codex-lb serve --data-dir /var/lib/codex-lb --listen 127.0.0.1:2455 \
    --trusted-proxy 127.0.0.1/32 --trusted-proxy ::1/128 --public-origin https://lb.example.com
WARN dashboard setup requires the one-time bootstrap token written to the data directory path=/var/lib/codex-lb/dashboard-bootstrap-token
INFO codex-lb ready address=127.0.0.1:2455
```

The administrator opens `https://lb.example.com`, enters the token from that file with the new password, and the file disappears. A second setup attempt with the same token returns 403 `invalid_bootstrap_token`.

## Failure modes

- Token file cannot be written (read-only data dir): `serve` stops with "cannot prepare dashboard authentication".
- Installation-wide delay during an attack can also delay the administrator for up to 15 minutes; already signed-in sessions keep working.
- An environment proxy excluded by `NO_PROXY` remains dialable by model sources as the operator's chosen egress.

## Deferred

Per-key memory and connection budgets, a listener connection limit, opt-in storage of error-archive bodies, a `trusted_header` identity allowlist, an audit-event table and separate encryption subkeys remain open; they touch the proxy hot path, capacity tuning or storage layout owned by other slices.
