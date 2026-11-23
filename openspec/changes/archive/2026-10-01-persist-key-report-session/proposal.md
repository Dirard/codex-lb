## Why

Refreshing the report page currently destroys the in-memory API-key login. A normal page reload must not sign out the holder or require storing their full generation credential in browser storage.

## What Changes

- Exchange a valid API key once for a separate encrypted, HttpOnly, read-only report-session cookie using the existing persistent cipher and session lifetime setting.
- Restore that session on page load and read reports through cookie-only routes; keep the public bearer report API unchanged.
- Revalidate key state and fingerprint on every session/report read. Clear the browser session on logout; distinguish authentication loss from temporary failures.
- Preserve scoped limits, group summaries, firewall policy, CSRF protection and administrator isolation.

## Capabilities

### Modified Capabilities

- `go-runtime`: Persistent, isolated browser authentication for API-key reports.

## Impact

Go application session validation and HTTP auth/report adapters; dashboard auth gate, report client and tests; go-runtime requirements and context. No database migration, new dependency, provider traffic or running-service update.
