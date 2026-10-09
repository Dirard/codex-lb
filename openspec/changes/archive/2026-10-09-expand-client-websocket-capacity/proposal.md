# Size client WebSocket capacity for standby pools

## Why

The 256 downstream-connection ceiling counts idle client pools as well as working sockets, although only response.create needs generation capacity. Many clients/subagents can therefore receive local_capacity_exceeded well below 256 active generations.

## What Changes

- Admit up to 4096 client WebSockets without adding a lower per-key socket ceiling; preserve independent upstream and generation limits.
- Bound retained large input frames independently from lightweight idle connections so enlarging the socket pool does not multiply the existing body backlog by sixteen.
- Preserve short cancellation messages, existing idle cleanup, authorization, continuation ownership and accounting.
- Measure the server process using the existing offline benchmark with standby sockets alongside active generation.

## Capabilities

### Modified Capabilities
- `go-runtime`: bounded downstream connection and incoming-message admission.

## Impact

HTTP adapter and existing tests/benchmark only. No schema, UI, new environment settings, quota or upstream routing changes. No commit, release, paid provider requests or live service update is included.
