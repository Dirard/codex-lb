# Codex route precedence and All-account verification

- Last edited with skill pack: `0.2.2`

## Title and scope

Restore native Codex WebSockets at the old base URL and verify that All-account key choices retain their existing authorization meaning.

## Planning anchor

Client logs on 2026-09-30 at 23:04 UTC show HTTP 409 during the WebSocket handshake, before any response.create frame. An authenticated diagnostic GET to the running Go server returned `realtime_call_owner_not_found` for `/backend-api/codex/responses`, while `/v1/responses` and the slash form returned the expected upgrade-required response. Amend go-runtime's composed-route contract and compatibility notes.

## Connected groups or observed existing logic

- ProxyHandler owns exact GET/POST Responses aliases in a private ServeMux. The command mounts that handler only as `/` on the outer proxy mux.
- CodexOperationsHandler registers GET `/backend-api/codex/{call_id}` directly on the outer mux. Its specificity wins before the private Responses routes can be examined.
- Add ProxyHandler.RegisterRoutes and reuse it for both its standalone mux and the command's shared mux. This is an actual existing composition boundary, not a new router abstraction. Keep Realtime paths/auth intact.
- All-account UI creation omits assignment arrays; explicit clearing submits an empty list. The dashboard save handler derives the scope flag from submitted arrays. SQL selection distinguishes unrestricted keys, group membership and restricted-empty deny-all keys. These rules must not be weakened to mask an availability error.
- Logs also show historical no-account errors, but the current unrestricted key has active candidates and a fresh real Luna request succeeded (11 input / 5 output tokens). The historical failure's exact policy snapshot is unavailable. Test create/select/clear/no-op and empty-group boundaries rather than inventing a data repair.

Focused source and runtime checks establish this local wiring defect; separate reverse documentation and a broader mapping artifact are unnecessary. No credential copying, working-server update, schema, retry or UI change is planned.

## Use cases

### 1. Route an old-prefix Responses request

shared proxy mux --match explicit method and Responses path--> Responses handler --authenticate and upgrade or parse--> existing request validation

Implementation Logic:
Register the same exact GET and POST patterns at the owning composition level. Use the current `/{$}` slash syntax. Do not remove the Realtime wildcard, add a catch-all switch, or change owner errors. Verify with openRuntime's actual assembled handler, an ephemeral key/store and real WebSocket client; invalid input avoids real-provider work.

Tests:
- description: Upgrade all four Responses path variants with Realtime routes installed.
  expected outcome: 101 handshake and existing validation error for an invalid frame; no call-owner lookup.
- description: Missing key, POST invalid input, and a non-owned Realtime call alias.
  expected outcome: Existing authentication, Responses validation and Realtime ownership remain distinct.

### 2. Preserve All-account key choices

admin creates unrestricted key --select eligible account--> scoped edit --explicitly clear selection--> unrestricted key --route new request--> permitted account

Implementation Logic:
Exercise existing admin POST/PATCH and Responses using an isolated SQLite store and a deterministic provider. Verify omitted or empty assignments on creation, selection and explicit clearing, unrelated edits, external source scope and empty group restrictions. No database migration or authorization broadening is justified without a failing regression.

Tests:
- description: Create a key with All, select one account and return to All.
  expected outcome: Default routing succeeds, explicit selection is honored, clearing disables only its assignment restriction.
- description: Empty restricted scope or group without members.
  expected outcome: No access to unrelated accounts and no upstream dispatch.

## Implementation checklist

1. [x] Identify the real 409 collision and inspect current All-key state without exposing credentials.
2. [x] Add a failing full-runtime route regression, then share exact route registration.
3. [x] Verify admin All-account transitions and closed-scope boundaries; fix only reproduced failures.
4. [x] Run focused/full checks, synchronize compatibility notes, and archive the verified change.

## Open questions

No blocking question for the reproduced route defect. Historical 503 state cannot be reconstructed from current key rows; report this limitation if the All-account checks pass. Deployment remains separately authorized.

## Decision log

Use the agreed implementation flow and smallest shared registration method. Skip a repeated formal review loop for this bounded repair; check both protocols, both path prefixes and security boundaries directly.

The assembled runtime regression failed with exactly the observed 409 before the registration change and passed afterwards for both prefixes/slash forms. Realtime alias ownership and missing-key checks remain enforced. All-account admin/proxy transitions passed without a product authorization change; the historical 503 cause is not claimed resolved. Two fixture defects (reusing a response ID across different owners and a duplicate group name) were corrected before the final test run.

Final validation passed: `go test ./...`, `go vet ./...`, focused race tests for the full-runtime Responses routes, All-account keys and Realtime, 35 API-key dialog tests, and ten real-binary dashboard contract tests. The user requested a Linux amd64 build, produced as `bin/codex-lb-go-2026.10.01-linux-amd64.tar.gz` with licenses and a checked SHA-256 sidecar; version `go-2026.10.01-route-fix`. The active local service was not replaced.
