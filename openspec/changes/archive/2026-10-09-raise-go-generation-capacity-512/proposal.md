## Why

The administrator approved shipping 512 simultaneous generations and 4096 client sockets in go-v1.0.8 after two mixed-input, single-CPU native-WebSocket trials completed without errors at 485–496 MB peak RSS. Leaving the application or upstream ceilings at 256 would make the published binary differ from that tested configuration.

## What Changes

- Raise default active generation admission, per-host HTTP transport and upstream WebSocket session capacity from 256 to 512.
- Preserve the 128-request queue, 15-second wait, per-account/key/source controls, ownership, accounting and 4096 downstream sockets.
- Make existing capacity regressions exercise 512 and publish Linux amd64/arm64 release packages with checksums and measured-workload caveats.

## Capabilities

### Modified Capabilities
- `go-runtime`: default concurrency and verification contract.

## Impact

Application defaults, runtime HTTP wiring, upstream socket ceiling, capacity tests and OpenSpec/release notes only. No schema, client setup, new setting, automatic service update or removal of provider/account limits. Commit, push to origin/fork and GitHub publication are explicitly authorized; deployment is not.
