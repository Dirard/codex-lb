# Publish the verified 512-generation configuration
- Last edited with skill pack: `0.2.2`

## Context and scope

See proposal.md. The new downstream socket budget and body-lease handling are already implemented and verified in expand-client-websocket-capacity. The administrator now explicitly selected 512 active generations for go-v1.0.8 and authorized commit, origin/fork push and publication. No running installation may be updated.

## Planning anchor and connected logic

NewProxy defaults to 256 active + 128 queued. openRuntime's MaxConnsPerHost is 256, and websocketSessions.acquire counts at most 256 live upstream lanes. The successful experiment changed exactly these three ceilings through a Go overlay. Body admission already derives from twice active-plus-queued capacity, so it becomes 1280 without a new constant. HTTP body readers, account/source/create/fair-share admission, continuation byte/count bounds and retry policies remain unchanged. Existing focused inspection and the measured overlay establish this path; additional reverse documentation and mapping are unnecessary.

## Use cases

### 1. Run an eligible generation within the larger global budget
authenticated request --apply unchanged key and capability policies--> permitted request --acquire the bounded 512-active admission--> admitted generation --use a matching HTTP or WebSocket transport slot--> upstream response --settle once and release owned resources--> available capacity

Logic Details:
- Change only the application default, per-host HTTP connection ceiling and upstream WS lane ceiling from 256 to 512. Preserve the 128-request queue and 15-second wait; no new configuration knob or abstraction.
- Keep idle-only eviction and exact continuation ownership. Existing linear scans remain bounded at 512; mixed tests show no need for an eviction index.
- Extend the current runtime HTTP and upstream WS capacity tests to 512; keep application queue/cancellation regressions and update derived body-capacity expectations. Retain the earlier 256-subscription regression.

Tests:
- description: The real runtime HTTP transport admits 512 concurrent streams to one host; native upstream connections retain independent response ownership and all resources close on cancellation/shutdown.
- description: Defaults are 512 active, 128 queued and 1280 large body leases; configured smaller capacities and existing validation/auth/accounting still work.
- description: The release binary repeats the mixed native-WebSocket load on one CPU, confirming actual 512 overlap, returned content, usage and measured resource consumption.

### 2. Publish matching release artifacts without deployment
verified working tree --commit the authorized changes--> source commit --build static amd64 and arm64 with embedded dashboard--> checked packages --publish source tag and checksummed GitHub assets--> go-v1.0.8 release

Logic Details:
- Preserve SQLite schema 33 and update protocol 1. Package codex-lb, required license texts and the optional existing service template using established names. Do not edit CHANGELOG.md or production configuration.
- Validate amd64 version/update-info and offline dashboard contracts; arm64 is cross-compiled and ELF-inspected, not declared hardware-tested.
- Record mixed-profile results and larger-context limits in release notes. Raising the ceiling does not make 512 maximum-size inputs fit 500–600 MB or override upstream quotas.

## Implementation checklist

1. [x] Promote the three experimental ceilings and update focused tests/specs.
2. [x] Run Go/race/vet, UI build, binary integration, mixed-load regression and strict specification validation.
3. [x] Prepare checked release assets/notes and archive verified work. Then commit/push origin/fork, publish go-v1.0.8 and verify remote tag/assets under the separate publication authorization.

## Decisions and limitations

- Use the already measured configuration rather than adding dynamic sizing, disk spooling or new settings. Two earlier mixed trials peaked at 485–496 MB; CPU was about 53% of one core. These are synthetic workload results, not a universal SLA.
- Specification completeness/consistency check is local: the scope is a small promotion of a tested configuration, not new architecture. No blocking questions remain; implementation and publication are authorized.
- Final Go, full race and vet checks passed. The capacity tests additionally passed three times under race. UI built and all 11 offline binary integration tests passed. The actual static go-v1.0.8 amd64 binary ran the same native mixed load: 512 active streams, zero errors, 466.04 MiB peak RSS, 53.44% steady single-core CPU, verified content/accounting and cleanup. Both static architecture packages contain the approved four files and passed SHA-256 verification; schema 33/update protocol 1 are unchanged. ARM execution and production deployment remain out of scope.
