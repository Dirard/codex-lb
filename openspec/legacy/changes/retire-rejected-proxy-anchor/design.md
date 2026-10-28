## Context

See [proposal.md](proposal.md). The proxy already records rejected response IDs in a short-lived negative cache, but direct-WebSocket anchor injection reads the separate session continuity state. Because the rejected ID remains `last_completed_response_id`, retries keep recreating the same invalid request.

## Goals / Non-Goals

**Goals:**

- Remove only the rejected proxy-injected anchor from the live continuity state.
- Preserve a newer completion written by another request.
- Preserve the upstream error for the failed turn and let the next full request start without the stale anchor.

**Non-Goals:**

- Retry an unsafe or incomplete request automatically.
- Clear the whole continuity cache, restart the process, or change client-supplied response handling.
- Change account routing or HTTP-bridge continuity.

## Decisions

### Compare-and-clear the live anchor

The upstream error path receives both the request state and the shared live continuity state. When the rejected ID was proxy-injected, it clears `last_completed_response_id` and its dependent input/tool metadata only if the live value still equals that exact ID. This compare-and-clear prevents an older failed request from erasing a newer successful completion.

Retirement happens immediately after classifying `previous_response_not_found`, before choosing between a verified fresh replay and a terminal rewrite. A later reconnect or replay failure therefore cannot leave the rejected anchor available for the next request.

The failed turn still returns the normalized `previous_response_not_found` error. Recovery begins with the next client request, whose complete payload is no longer trimmed against the rejected anchor.

## Risks / Trade-offs

- The next request may send more input because it cannot reuse the rejected response; this is required for recovery.
- A client-supplied invalid response remains the client's responsibility and is not silently removed.

## Migration Plan

No migration is required. Deploy the updated image; rollback uses the previous image.
