# Codex Realtime transport and privacy repair

- Last edited with skill pack: `0.2.2`

## Title and scope

Restore the retained Realtime contract on the Go proxy without replacing the working installation or selecting another generation model.

## Planning anchor

Original `app/modules/proxy/api.py` accepts opaque bodies and uses private Realtime errors. Its integration tests cover SDP offers, Location binding and credential-free failures. Official Codex `codex-rs/codex-api/src/endpoint/realtime_call.rs` sends raw SDP, backend JSON or multipart and uses `intent`/`architecture` query parameters. Go currently reads JSON only, forces its MIME and drops the query. Its relay bounds reads to 4 MiB but never sets either peer's library read limit; all WebSocket close codes are treated as normal.

## Connected groups or observed existing logic

- `httpapi.createRealtimeCall` authenticates, reads the offer, and calls `CodexOperations.CreateRealtimeCall`; pass the existing typed `CodexControlRequest` with body, media type and query rather than inventing another transport DTO. Method/path are fixed inside the use case.
- Application owner selection and `contentFree` own admission, statistics and diagnostic capture. The private call wrapper must replace failures before they reach statistics and skip payload diagnostics for call creation. Use bounded cancellation-independent cleanup for content-free writes, matching the existing billed path.
- `provider.Control` already forwards raw bytes, MIME and query through the common authenticated operation flow; no new HTTP client or auth retry is necessary. Its 401 handling retains existing output/billing guards.
- `realtimeCallID` currently strips query and rejects fragments. Parse Location as a URL and extract/validate its escaped final path segment; neither query nor fragment is ownership state. Preserve the original successful Location for the client.
- `httpapi.realtimeLive` and `provider.Realtime` must both set the same existing 4 MiB policy. Keep two owned relay tasks, cancellation and joining; bound close propagation and classify 1000/1001 as normal, other upstream close codes as failures.
- Existing provider and route tests cover small frames and owner persistence only. Extend actual provider-backed tests for call bodies/query, privacy, large/oversized frames, abnormal close and cancellation.

Focused code and original tests already establish behavior, so no separate reverse-document artifact or broad repository map is required. Existing go-runtime ownership requirements remain unchanged; this change amends its transport/privacy coverage.

## Use cases

### 1. Create a private Realtime call

authenticated offer --validate bounded media and query metadata--> scoped account --forward unchanged offer--> upstream answer --persist scoped call owner--> client SDP and Location

Implementation Logic:
Use the existing bounded body reader. Accept the original request media types with full parameters and reject unsupported Content-Encoding. The application validates size/query limits and fixes method/path before provider dispatch. On success parse the call ID without storing Location query/fragment and await owner persistence. Any upstream or binding error becomes a fixed private failure before content-free statistics. Call payloads never enter diagnostic archives.

Tests:
- description: Send actual SDP, JSON and multipart bytes with repeated query values through authenticated routes.
  expected outcome: Upstream sees unchanged bytes/MIME/query, successful answer is returned, and later sideband ownership is scoped to the same key/account.
- description: Return an upstream failure containing SDP/ICE, arbitrary codes and sensitive headers or fail owner persistence.
  expected outcome: Safe fixed errors and sanitized statistics, with no diagnostic payload and no successful owner publication.

### 2. Relay bounded sideband frames

authorized owner --open upstream sideband--> two owned relay tasks --copy bounded frames--> normal, oversized or failed close --join tasks and persist outcome--> released admission

Implementation Logic:
Expose one shared 4 MiB constant to both adapters. Set each WebSocket reader limit explicitly and retain bounded reading. Preserve normal close handling for 1000/1001 and cancellation; map oversized data to a safe size close and abnormal upstream failures to a sanitized error. Never forward provider close reasons. Content-free completion uses the existing cancellation-independent bounded write pattern.

Tests:
- description: Relay 128 KiB text and binary messages through the real handler and adapter.
  expected outcome: Both directions preserve exact bytes without the former 32 KiB disconnect.
- description: Send an oversized frame, close upstream with 1011, and cancel a downstream connection.
  expected outcome: Bounded termination, joined tasks, released admission and one correctly classified content-free outcome.

## Implementation checklist

1. [x] Compare original/current client contracts and trace privacy and relay ownership.
2. [x] Repair bounded call forwarding and private errors/diagnostics.
3. [x] Apply both peer frame limits and correct abnormal close classification.
4. [x] Verify route, provider, privacy, cleanup and race tests; run full tests.
5. [x] Synchronize and archive verified requirements and verification limits.

## Open questions

A live WebRTC peer is not available in this environment; do not claim a real media call from mocked peers. Real subscription transcription and file-backed Luna image flows have independently passed. No production restart is authorized.

## Decision log

- The active SDD implementation flow authorizes this bounded compatibility repair. Completeness/consistency checks include privacy and both peer limits because changing only the call MIME would leave retained Realtime functionality unusable for larger frames.
- 2026-09-28: Actual HTTP handlers, provider adapters and SQLite passed JSON/SDP/multipart forwarding, repeated query values, fragment-free owner binding, invalid request limits and private upstream/binding error checks. Actual WebSocket peers passed 128 KiB text/binary round trips and abnormal close, abrupt disconnect, oversized frames in both directions, cancellation accounting and released admission. Payload diagnostic callbacks were not invoked. Focused race tests, full `go test ./...` and `go vet ./...` passed. No real WebRTC media call is claimed.
