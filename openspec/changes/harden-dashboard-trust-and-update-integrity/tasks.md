## 1. Explicit trust

- [x] 1.1 Remove request locality from identity resolution; bootstrap setup always requires a token.
- [x] 1.2 Generate, publish (0600) and consume one-time bootstrap tokens; persist consumed operator token digests; re-arm after password removal.
- [x] 1.3 Keyless proxy access only for `--unauthenticated-client` networks resolved through trusted proxies; reject overlap with `--trusted-proxy`; warn for loopback networks.
- [x] 1.4 Warn once about forwarded headers from an untrusted loopback peer; add `--public-origin`; trust the loopback proxy in `deploy/codex-lb.service`.

## 2. Authentication hardening

- [x] 2.1 8 KiB body limit for `/api/dashboard-auth/*` and `/api/key-reports/session`; 413 `request_too_large`.
- [x] 2.2 bcrypt outside the state mutex with four hashing slots and immediate 429 `authentication_busy`.
- [x] 2.3 Per-client (IPv6 /64) and installation-wide failure limits with exponential delay and `Retry-After`; rejection logging without secrets.
- [x] 2.4 Require configured TOTP at sign-in; settings flag is read-only.
- [x] 2.5 Session v2 with id, epoch, idle deadline and renewal; logout revocation; `POST /api/dashboard-auth/logout-all`; 30-day cap; migration 34.
- [x] 2.6 Password re-entry for account token export and runtime apply/rollback.
- [x] 2.7 `disabled` dashboard auth only with a loopback listener, with a startup warning.

## 3. Outbound and browser policy

- [x] 3.1 Reject non-public model-source base URLs and restrict model-source dials to public addresses (`--allow-private-model-sources` opt-in).
- [x] 3.2 CSP for dashboard and proxy routes, HSTS in secure mode, `__Host-` admin cookie, `SameSite=Strict`.
- [x] 3.3 Extend error-archive redaction patterns.

## 4. Release integrity

- [x] 4.1 Verify `SHA256SUMS.sig` against embedded ed25519 keys before downloading the archive; checksum-only mode while no key is embedded.
- [x] 4.2 `cmd/release-sign` (`sign`, `verify`, `public-key`) and the signing step in `.github/workflows/release.yml`.
- [ ] 4.3 Administrator: generate the signing key, embed its public key, configure `RELEASE_SIGNING_KEY` (see `context.md`).

## 5. Dashboard

- [x] 5.1 Bootstrap screen and setup dialog name the token source.
- [x] 5.2 TOTP settings state the requirement instead of a toggle.
- [x] 5.3 Warning on every page while API-key authentication is off.
- [x] 5.4 "Sign out everywhere" in session settings; password prompts for export and runtime updates.

## 6. Verification

- [x] 6.1 Route-level tests (httptest) for bootstrap, keyless access, cookies/headers, body limits, failure limits, TOTP, revocation, model-source destinations and re-authentication; adapter tests for the dial guard and release signatures.
- [x] 6.2 `go vet`, `golangci-lint`, `go test`, `go test -race` (touched packages), dashboard lint/typecheck/vitest, strict OpenSpec validation.
