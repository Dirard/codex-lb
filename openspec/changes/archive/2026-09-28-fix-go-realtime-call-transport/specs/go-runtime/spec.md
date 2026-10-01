## ADDED Requirements

### Requirement: Codex Realtime preserves private call transport and bounded sideband messages

The authenticated Realtime call route SHALL preserve supported JSON, SDP and multipart request bytes, complete Content-Type parameters and bounded query values when forwarding to the subscription account. Unsupported encodings, malformed media-type metadata and oversized requests SHALL fail before dispatch. Successful SDP and Location responses SHALL be returned only after key-scoped call ownership is durable; query and fragment values in Location MUST NOT enter the call identifier. Realtime request/response payloads MUST NOT be archived as diagnostics, and upstream failure bodies, headers and arbitrary codes MUST NOT appear in client errors or content-free request statistics. Existing key/group/account authorization SHALL remain enforced.

Both sideband peers SHALL accept messages up to the shared 4 MiB bound and reject larger messages without unbounded buffering. Normal close and cancellation SHALL release relay tasks and admission; an abnormal upstream close MUST NOT be reported as successful completion. Accounting SHALL record a sanitized terminal outcome after cancellation without using the cancelled request context for its cleanup write.

#### Scenario: Codex creates a private call
- **WHEN** an authenticated client sends a supported bounded JSON, SDP or multipart offer with query parameters
- **THEN** upstream receives the original bytes, full media type and query, and the client receives the SDP answer and Location after durable owner binding

#### Scenario: Call creation fails with private content
- **WHEN** upstream rejects a call, a transport fails, or owner binding cannot be committed
- **THEN** only a fixed safe failure code/message and appropriate HTTP status are exposed
- **AND** neither SDP/ICE content, response error text nor Location secrets enter diagnostics or request-log error fields

#### Scenario: A sideband transports a larger audio/control frame
- **WHEN** a permitted peer sends a valid frame larger than 32 KiB but no larger than 4 MiB
- **THEN** it is relayed unchanged in both directions
- **AND** an oversized message or abnormal upstream close terminates safely with a failed content-free outcome, not an artificial successful close
