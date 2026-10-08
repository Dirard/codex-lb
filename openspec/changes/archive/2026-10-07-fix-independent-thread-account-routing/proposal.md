# Proposal

## Why

Independent Codex threads can share a prompt-cache key. The current soft-affinity lookup ignores their Thread-Id, so the first thread's account overrides capacity-weighted or round-robin selection for its siblings. Parallel sockets do not correct this account-selection coupling.

## What Changes

- Scope explicit prompt-cache affinity by the logical session/thread when Thread-Id is present, preserving the provider-facing cache key.
- Let new independent threads enter the configured account selector without inheriting a sibling's cache pin.
- Retain exact previous/session/turn/file ownership, quota-only migration and anonymous-client cache affinity compatibility.
- Exercise six-account routing through HTTP and WebSocket, as well as the shared compact selection path.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `go-runtime`: Independent logical-thread cache affinity and unchanged hard-owner continuity.

## Impact

Shared application affinity classification, existing proxy/compact callers and regression tests. No DB migration, new dependency, setting, live-data cleanup, release or service update. Manual account policy, earlier-reset preference and weighted selection semantics remain unchanged.
