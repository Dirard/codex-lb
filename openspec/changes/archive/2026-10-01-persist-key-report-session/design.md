# Persistent key-report login

- Last edited with skill pack: `0.2.2`

## Title and scope

Refreshing the report page must not sign the key holder out. Keep a restricted browser session without persisting the generation credential, while preserving revocation and read-only scope.

## Planning anchor

`AuthGate` holds `KeyReportSession.apiKey` only in React state; `getKeyReport` sends it on every bearer report request. A reload destroys that state. Amend `openspec/specs/go-runtime/spec.md` and its context; preserve the public bearer API and all existing group/limit semantics. No schema migration, service deployment or publication is in scope.

## Connected groups or observed existing logic

- Entry and consumers: `auth-gate.tsx`, `key-login-form.tsx`, `key-reports-page.tsx` own login choice, ephemeral key and private query cache. Restore session status before mounting either admin or reports. Never write key credentials into the admin store.
- Authentication and persistence: `admin_auth.go` already uses `SecretCipher`, a persistent encryption key and bounded stateless sessions. A separate application use case validates report grants against `GetAPIKey` on every read; reuse `secretTag` and the existing HTTP session TTL/cookie security policy. No new tables or dependency.
- HTTP and scope: `server.go` registers public routes outside admin middleware; `key_reports.go` binds filters to an authenticated key. Share its report response function between bearer and cookie routes. The latter enforce firewall for login and reports, but status/logout must remain available for repair and clearing a session.
- Validation: existing `key_reports_test.go`, application auth tests, frontend report tests and auth mocks provide the test harness. Use synthetic data and the embedded UI in a temporary local server for browser reload verification.
- Specs: amend the memory-only login requirement; historical validation notes remain historical. The previous common-login and group-summary contracts otherwise stay authoritative.

## Use cases

### 1. Create and validate a restricted report session
API-key submission --authenticate bearer and client--> accepted key --encrypt read-only grant--> report cookie --revalidate grant and current key--> report principal

Implementation Logic:
Use an application `KeyReportAuth` with a narrow `GetAPIKey` port and the existing cipher. Payload contains a distinct kind, key ID, hash fingerprint and absolute expiry, never the original API key. Authenticated encryption already supplies randomness. Verify bounded token size, purpose, expiry, current key state and constant-time fingerprint equality. Only invalid credentials return unauthorized; storage failures propagate as server errors. Key budget exhaustion is not an authentication failure. Issue with the existing dashboard TTL capped by the key expiry. `POST /api/key-reports/session` consumes bearer credentials; `GET` checks the cookie; `DELETE` clears it. Cookie name and path differ from admin, with HttpOnly, SameSiteLax and trusted HTTPS Secure behavior.

Files And Functions:
 - planned: internal/application/key_report_auth.go#KeyReportAuth - issue and verify scoped session grants
 - planned: internal/adapters/httpapi/key_report_session.go - public session routes and cookie-only reports
 - existing: internal/adapters/httpapi/auth.go#sessionTTL - existing session lifetime and trusted HTTPS policy
 - existing: internal/adapters/httpapi/proxy_auth.go#authenticateBearerKey - narrow lookup port, reuse strict bearer validation

Tests:
 - description: Session survives reconstructing the server with the same store/cipher; invalid, altered, wrong-purpose, rotated, expired and revoked credentials fail; storage errors do not become false logout.
 - description: Secure/path/HttpOnly attributes, CSRF, firewall and cookie rejection on admin/bearer routes remain enforced.

### 2. Read scoped reports after reload
dashboard load --restore public session states--> [report session --read scoped report--> report view, administrator session --retain existing admin flow--> admin view, no session --offer common login--> login, temporary failure --offer retry--> recoverable state]

Implementation Logic:
Read admin and report status concurrently at bootstrap, wait for both, and prefer an already authenticated administrator. Do not mount admin children before restoration completes. Remove `apiKey` from report state; initialize the report date filters on mounting. Cookie-only `GET /api/key-reports/reports` calls the same scoped report response as `/v1/usage/reports`; no duplicate aggregation. Reports use same-origin credentials, no-store, a private query client, and suppress admin unauthorized handling. A 401 hides data and returns to key login; 5xx/network failures retain the session with retry. Login clears the key draft after POST and blocks method switching during that POST.

Tests:
 - description: UI remount and actual browser reload restore reports without persisting the key or sending it again; admin routes and peer traffic are not requested.
 - description: Existing own/group limits remain rendered; report and bootstrap failures can recover by retry without logging in again.

### 3. Sign out and change credentials
report view --delete report cookie--> [successful deletion --cancel reads and clear private cache--> empty key login, failed deletion --show retryable error--> report session]

Implementation Logic:
Await the CSRF-protected DELETE before claiming successful logout. Clear only the report cookie; unmounting cancels queries and clears their cache. Auth failures also leave no visible old report. Do not add a revocation database: as with admin sessions, logout clears the browser grant, while key deactivation/rotation/deletion invalidates grants server-side.

Tests:
 - description: Logout followed by reload stays signed out; a failed logout remains retryable and does not leak another key's cached report on next login.

## Implementation checklist

1. [x] Plan persistent report login and delta requirements.
2. [x] Implement application validation and HTTP cookie session/report routes with regression tests.
3. [x] Restore browser sessions, update login/logout flow, localization and frontend tests.
4. [x] Run Go tests/race/vet, UI tests/build and real browser reload verification on synthetic data.
5. [x] Synchronize go-runtime requirements/context and archive the verified change.

## Open questions

None blocking. The existing dashboard session lifetime is reused rather than introducing another setting.

## Decision log

- Reverse documentation skipped: focused code inspection already established the exact loss-of-state cause. Connected mapping above covers the cross-boundary security change.
- Completeness/consistency check: scoped session, wrong-purpose rejection, storage-error handling, firewall repair access and pending-login race are included. No unrelated authentication redesign.
- Stateless encrypted grants reuse existing cryptography and survive restart; local/sessionStorage of the API key would expose generation authority to browser script. New server-side session tables are unnecessary for reload persistence.
- Logout invalidates the browser cookie, not copied/compromised stateless grants. Key revocation/rotation invalidates all such grants; HTTPS remains required remotely.
- Cookie-only routes preserve the bearer API contract. Deployment and release require separate authorization; rollback has no data migration.
- Verification: `go test ./...`, `go test -race ./...`, `go vet ./...`, focused ESLint, UI typecheck/build and all 1241 UI tests passed. The 10 opt-in contracts also passed against the freshly built Go binary.
- Actual Chromium verification used synthetic keys and a temporary runtime: login, page reload, server stop/start with the same data directory, network failure/retry, logout plus reload, switching to a different key and credential regeneration all passed. API keys were absent from cookies and browser storage and sent only to session creation; no administrator data requests occurred. Screenshots were inspected. Temporary test processes were stopped; the working local service was not updated.
- Strict validation passed for this change and the amended go-runtime spec; SDD structural validation passed. Whole-repository strict OpenSpec validation retains 22 unrelated pre-existing historical-spec failures (38 of 60 specs pass); they were not changed for this fix.
