## Context

Routed upstream Responses sockets reuse the downstream 120-second idle budget as their aiohttp heartbeat interval, while production evidence shows an otherwise idle routed connection closing without a close frame after about 63 seconds. Direct `websockets` connections already use the library's proactive default ping cadence.

## Goals / Non-Goals

**Goals:**

- Keep healthy routed upstream sockets alive before the observed idle boundary.
- Preserve downstream and direct transport semantics.

**Non-Goals:**

- Adding another operator setting or changing downstream idle semantics.
- Overriding the direct transport's native ping cadence.

## Decisions

### Derive a routed upstream heartbeat cadence

Use the smaller of 30 seconds and half the configured downstream idle budget for routed aiohttp heartbeats. Keep the existing downstream budget as the pong/liveness timeout. This sends a ping before a roughly 60-second upstream idle expiry without adding configuration. Leave direct `websockets` connections on their native proactive default.

Alternative: lower `proxy_downstream_websocket_idle_timeout_seconds`. Rejected because that setting also governs downstream client idleness and would close quiet clients earlier.

## Risks / Trade-offs

- [More ping frames on idle routed sockets] → Frames are small transport controls and consume no model credits; cap cadence at 30 seconds.

## Migration Plan

No schema migration. Deploy normally; rollback restores the prior routed heartbeat interval.
