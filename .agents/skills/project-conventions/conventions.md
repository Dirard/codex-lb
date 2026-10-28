# Project Code Conventions

The repository is a single Go module (`codex-lb`) with an embedded React dashboard in `web/`.

## 1. Layers

- `internal/domain`: transport- and storage-independent rules and types. Imports only the standard library.
- `internal/application`: use cases. Each use case owns the narrow ports it consumes; it never imports adapters or `net/http`.
- `internal/adapters/*`: concrete SQLite, HTTP, upstream/provider and update implementations of those ports.
- `cmd/codex-lb`: configuration parsing and wiring. Environment variables are parsed here and passed down as typed options.
- Do not add an interface without a real architecture or testing boundary.

## 2. Contracts and data

- Use typed structs for known contracts. Keep unknown provider fields as `json.RawMessage` only at a protocol boundary.
- Validate external input once at the entrypoint and rely on the validated contract afterwards.
- Expose dashboard times as RFC 3339 strings, money as integer microdollars.
- One canonical name per setting or environment variable; no speculative fallbacks. Fail fast on invalid configuration and name the offending setting in the error.
- Do not store derived state that can be recomputed from the source of truth.

## 3. Concurrency, I/O and storage

- Every request-owned goroutine has an owner that cancels and joins it; background loops recover panics, log them and stop with the service.
- Pass request-scoped `context.Context` to all I/O. Never hold a lock or an SQLite transaction across upstream network I/O.
- Keep SQLite transactions short and route durable writes through the store's batch writer.
- Bound every read (body size, frame size, collection size) and every wait (timeouts, queue depth).

## 4. Security and money

- Credentials stay encrypted at rest; never log tokens, keys, request bodies or provider error bodies. Log stable error codes and identifiers instead.
- Preserve API-key reservation and settlement semantics: reserve before dispatch, settle exactly once, keep unknown usage reserved for reconciliation, never replay an attempt that may have executed.
- Account ownership: file-pinned and provider-state-bound requests never move between accounts; failover requires an explicit upstream quota refusal.

## 5. Tests

- Tests live beside the implementation. Use deterministic local upstream stubs and temporary SQLite databases; never real provider credentials or traffic.
- A bug fix needs a regression test on the failing product path (route, WebSocket, migration, dashboard), not only on a helper.
- Prefer channels or `testing/synctest` over `time.Sleep` polling.
- Gates: `make check` (vet, lint, test, race, vuln, OpenSpec) and `make web-check`.

## 6. Migrations

- SQLite migrations in `internal/adapters/sqlite/migrations.go` are append-only and atomic. A new version must upgrade every earlier supported version, backfill historical rows when a new column affects them, and keep `PRAGMA user_version` equal to the migration count.
- Downgrade is restore-from-backup; document it in the release notes when the schema changes.
