## Context

Upstream `82a58aff` introduced native Codex traffic handling and deliberately propagated synthetic transport failures out of the HTTP response iterator. In a local Uvicorn reproduction, the same simulated upstream failure produces a complete `response.failed` stream with stable normalization, but a `200 OK` followed by `ClientPayloadError` with native lifecycle preservation. The new packaged egress client also timed out intermittently during unauthenticated network checks; those checks do not establish its cause or authorize replay of dispatched requests.

## Goals / Non-Goals

Restore actionable, correctly framed client errors and downstream liveness. Keep account ownership, quota-only failover, reservation settlement, and upstream dispatch safety unchanged. No live rollout, database mutation, speculative retry, or wholesale upstream revert.

## Decisions

Use the existing startup HTTP-error response and public SSE failure normalizer for native clients too. Keep the service's transport-failure and no-replay decisions; only the downstream presentation stops aborting HTTP bodies. Restore existing keepalive wrappers rather than introducing a timer or configuration setting.

Development Compose already rebuilds Python dependency and Dockerfile changes. Extend that existing watch list to the Cargo manifest, lockfile and crate sources; syncing Python alone cannot replace a compiled helper. No production container is changed by editing the development manifest.

The audit also exposed a fork quota-replay import that bypassed the upstream architecture boundary. Route that existing helper through the existing service facade, preserving its arguments and behavior. The native deadline/socket, weekly-demand baseline and bridge cancellation corrections are recorded in their separate changes.

For example, upstream EOF after `response.created` produces one terminal `response.failed` and a clean end of the HTTP body, never a fabricated `response.completed`. A failure observed by the startup probe before HTTP commitment remains a non-2xx JSON error.

## Risks / Trade-offs

Correct framing does not repair upstream connectivity. Tests must distinguish a reported failure from successful inference, and must verify that no extra upstream attempt or reservation is created. Verify the real local HTTP wire in addition to iterator and route tests, since a mocked readiness check cannot detect truncated chunked bodies.

## Migration Plan

No migration is needed. Keep the working container on the user-selected rollback until a separately approved deployment.
