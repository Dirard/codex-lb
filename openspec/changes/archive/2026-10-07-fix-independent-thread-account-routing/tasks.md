# Tasks

## 1. Scoped account affinity

- [x] 1.1 Add a failing regression for named threads sharing an explicit cache key, then scope the shared classifier by session/thread; verify distinct keys, TTL, bounded input and anonymous-client compatibility.
- [x] 1.2 Exercise six-account HTTP and WebSocket routing plus compact selection with local fixtures; verify hard-owner continuity and exact accounting despite changed cache hints.
- [x] 1.3 Synchronize the go-runtime contract/context with the actual scoped-affinity rule, migration boundaries and unchanged weighted/operator policies.

## 2. Verification and handoff

- [x] 2.1 Run go test ./..., go test -race ./... and go vet ./...; validate the changed spec/design and archive only verified work without deployment or publication.
