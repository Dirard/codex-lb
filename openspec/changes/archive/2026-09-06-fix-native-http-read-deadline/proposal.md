## Why

Native HTTP currently starts the configured response-read watchdog as soon as the request command reaches the helper. Unlike aiohttp `sock_read`, that timer also consumes helper scheduling and connection establishment, so a valid connection phase can exhaust the entire read budget before upstream has a chance to return response headers.

## What Changes

- Give native HTTP the configured connection allowance before its response-head read allowance, while retaining the overall request deadline.
- Keep post-dispatch timeout failures terminal and preserve the existing endpoint, TLS, and replay-safety contracts.
- Add real local wire coverage for a connection phase longer than the read budget and for a stalled response head.
- Abort only the affected native WebSocket when its bounded command channel reports a terminal dispatch error, so the Rust task cannot outlive its Python owner.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `outbound-http-clients`: Native HTTP response-head deadlines preserve the existing connection/read phase budget instead of collapsing both into the read timeout.

## Impact

The persistent native Python adapter, the Rust WebSocket command dispatcher, and focused local tests. No database, deployment, setting, public API, or helper protocol change is required.
