## Context

aiohttp starts `sock_read` after the request writer reaches EOF, separately from `sock_connect`. The native adapter instead waits for the first helper event with `response_head_timeout_seconds` immediately after writing the IPC command, so DNS, TCP/TLS/proxy setup, request dispatch, and helper scheduling all spend the response-read budget. With both settings at eight seconds, native can fail at roughly eight seconds where aiohttp permits the connection phase and then the read phase.

Reqwest 0.12.28 `ClientBuilder::read_timeout` does not restore that separation: its `PendingRequest` creates and polls the read timer before the in-flight request has completed connection establishment. Adding that builder option alone would retain the same regression.

The native WebSocket dispatcher has a separate ownership leak. When a socket's bounded command channel is full, it emits a terminal `websocket_error`, but leaves the corresponding Rust task in the active registry. Python consumes that terminal and unregisters its demultiplexing queue, so the orphaned socket can continue running while all later events are discarded.

## Goals / Non-Goals

Restore the pre-native connection/read deadline envelope and keep a finite response-head watchdog. Preserve the total request budget, TLS verification, endpoint selection, cancellation, and no-replay behavior after ambiguous dispatch. Ensure a terminal WebSocket command-dispatch error tears down only its owning socket. Do not add a setting or a protocol field that the current helper cannot enforce at the correct phase.

## Decisions

When both budgets are present, the Python adapter waits at most `min(total, connect + response_head)` for the first helper event. If either phase budget is absent, the total request deadline remains the only safe bound. This is the smallest enforceable envelope because the current helper protocol has no post-connect event.

For example, a 200 ms proxy handshake with a 500 ms connect budget and a 100 ms read budget remains valid. An origin that accepts the request but never returns headers is still cancelled within the 600 ms combined bound (or sooner at the total deadline), and that timeout remains non-replayable.

When `try_send` reports a full or closed WebSocket command channel, remove that request id from the active registry and abort its existing task before emitting the terminal error. Other multiplexed request ids and sockets remain untouched. Python already fails every pending acknowledgement on the terminal error, so no new wire event or replay path is needed.

## Risks / Trade-offs

A pooled connection may receive the unused connect allowance before the response-head watchdog fires. The wait remains bounded by the total deadline; exact phase timing would require a trustworthy post-dispatch signal below reqwest's current request future. Real-wire tests cover both sides of the chosen envelope. Aborting a socket after command-channel overflow intentionally drops its already-queued frames because Python has already received a terminal failure and cannot safely resume or replay them.

## Migration Plan

No migration or rollout action is required. The helper wire contract remains protocol version 1.
