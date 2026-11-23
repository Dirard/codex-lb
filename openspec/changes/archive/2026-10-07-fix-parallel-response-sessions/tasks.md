# Tasks

## 1. Upstream connection leases

- [x] 1.1 Reproduce shared-session contention with deterministic local WebSocket tests, then implement bounded independent leases; verify 256 concurrent same-session requests, exact continuation reuse and real overflow rejection.
- [x] 1.2 Verify cancellation/retirement, credential and ownership isolation, and required-capability transitions with focused race tests; keep the design's connection rules synchronized.

## 2. Downstream admission and visible behavior

- [x] 2.1 Separate bounded downstream sockets from HTTP body readers; verify 256 idle sockets, HTTP progress, rejected overflow and released capacity.
- [x] 2.2 Add HTTP/SSE and WebSocket parallel-branch integration regressions using the real provider adapter and SQLite fixture; verify distinct outputs, exact dispatch counts and settled accounting without an account switch.

## 3. Integration and handoff

- [x] 3.1 Run go test ./..., go test -race ./... and go vet ./...; resolve task-related failures without deployment or provider probes.
- [x] 3.2 Synchronize go-runtime spec/context, validate the change and layered design, and archive the verified change; report remaining real capacity limits and that running services are unchanged.
