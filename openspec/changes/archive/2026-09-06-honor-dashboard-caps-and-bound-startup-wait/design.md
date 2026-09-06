## Context

See [proposal.md](proposal.md) for motivation. The two failing paths already have the required primitives: warm bridge reacquisition can accept explicit `AccountConcurrencyCaps`, and startup probing already computes one signal-discovery deadline before observing capacity markers.

## Goals / Non-Goals

**Goals:**

- Make warm bridge reacquisition use the same cached dashboard cap snapshot as initial admission.
- Ensure every capacity-aware startup-probe branch returns within the existing discovery deadline without cancelling the underlying stream.

**Non-Goals:**

- Change cap, recovery-reserve, fair-share, retry-cadence, or request-budget semantics.
- Add settings, background recovery, cross-account replay, or database state.

## Decisions

### Reuse the dashboard snapshot at the caller

Warm bridge reacquisition reads the dashboard settings cache once, derives `effective_account_concurrency_caps` from that snapshot, and passes the result into `acquire_account_lease`. The same snapshot supplies the API-key fair-share threshold. Fetching dashboard settings inside the load balancer was rejected because it would couple runtime accounting to persistence and introduce an await around its lock boundary.

### Bound the existing recovery wait with the existing deadline

The capacity-marker branch computes its remaining time from `signal_discovery_deadline` and passes that value to `asyncio.wait`. Expiry returns the existing `False` handoff result. A new timeout or setting was rejected because the function already defines the correct startup discovery budget.

### Hand off rather than cancel

Deadline expiry leaves the first-item task alive and hands it to `StreamingResponse`. The outer initial heartbeat becomes visible immediately while the inner capacity retry remains governed by its original request budget. Cancelling or replaying was rejected because the upstream request may still become admissible and replay could duplicate work.

## Risks / Trade-offs

- **A dashboard cache read now also occurs for unkeyed warm sessions** → the cache snapshot is obtained before load-balancer runtime locking and replaces a fallback that was already contractually invalid.
- **A late structured startup error may arrive after HTTP headers are committed** → this matches the existing bounded no-marker path; the error remains deliverable through the stream instead of blocking the client indefinitely.

## Migration Plan

No data migration is required. Deploy through the normal container replacement while preserving the existing data volume. Rollback restores the previous image; persisted settings and bridge rows remain compatible.
