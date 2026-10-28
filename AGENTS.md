# AGENTS

codex-lb is a single static Go binary (module `codex-lb`) with SQLite storage and an embedded React dashboard (`web/`). The retired Python/Rust runtime lives only in Git history.

## Architecture

- `internal/domain` — transport/storage-independent rules (stdlib only).
- `internal/application` — use cases; each owns the ports it needs. No adapter or `net/http` imports.
- `internal/adapters/*` — SQLite, HTTP API, upstream/provider, ChatGPT OAuth, releases/self-update, embedded web UI.
- `cmd/codex-lb` — configuration and wiring. Environment variables are parsed here.
- Prefer the standard library, typed contracts, request-scoped contexts, bounded I/O and short SQLite transactions. Never hold a lock or transaction across upstream network I/O. Do not add interfaces without a real architecture or testing boundary.
- Detailed coding rules: `.agents/skills/project-conventions/conventions.md`.

## Gates

| Command | Runs |
|---|---|
| `make check` | `go vet`, `golangci-lint` (`.golangci.yml`), `go test`, `go test -race`, `govulncheck`, `openspec validate --specs` |
| `make web-check` | dashboard lint, typecheck and vitest |
| `make web-deps build` | dashboard build embedded into `bin/codex-lb` |
| `make test-web-contract` | dashboard API contract tests against the built binary |

CI (`.github/workflows/ci.yml`) runs the same gates on every pull request and on `main`. A change is ready only when CI is green.

Tests sit beside the implementation and use deterministic loopback upstream stubs and temporary data directories. Real provider traffic (ChatGPT, Z.AI, OpenAI) and changes to a running installation need explicit operator approval.

## Specifications

- `openspec/specs/go-runtime/` is the normative product contract (`spec.md` holds testable requirements only; `context.md` holds rationale, examples and operating notes). Other folders in `openspec/specs/` are legacy references unless `go-runtime` cites them. `openspec/legacy/` is non-normative history.
- Behavior, API, schema, CLI, dashboard-visible, routing and compatibility changes need an OpenSpec change in `openspec/changes/<kebab-name>/` (proposal, tasks, spec delta) before coding. Validate with `make spec-validate`; archive to `openspec/changes/archive/YYYY-MM-DD-<name>/` after verification and sync the delta into `openspec/specs/`.
- Implementation planning for non-trivial work lives in solution specs under `specs/` (lifecycle: `specs/spec-lifecycle/workflow.md`). A solution spec plans the slice and its checklist; the OpenSpec change records the normative behavior. Keep both in sync with the code.

## Invariants reviewers check

- Proxy failover and retries: file-pinned or provider-state-bound requests never cross accounts; failover requires an explicit upstream quota refusal before anything was visible to the client; excluded accounts leave the selection loop; an attempt that may have executed is never replayed.
- Money: API-key reservations are taken before dispatch and settled exactly once before account-health writes; unknown usage stays reserved for reconciliation; idle disconnects are account-neutral.
- Concurrency: every spawned goroutine is cancelled and joined on all paths; partial failures keep finalization; fan-out is bounded; tests cover partial failure, not only success.
- Migrations: append-only, atomic, upgrade every earlier version, backfill rows affected by new columns; after migrating `PRAGMA user_version` equals the migration count. Mark a migration additive (`additiveMigrations`) only when older code keeps working on its result and it finishes in a few seconds (no table rewrite or large backfill): an additive migration runs while the previous version still serves, whose writes wait for the lock only within their own deadlines (settlement 10 s).
- Security: credentials encrypted at rest; no tokens, keys, bodies or provider error bodies in logs; trusted forwarding only from configured proxies.
- Bug fixes include a regression test on the externally failing path (route, WebSocket, CLI, migration, dashboard).

## Git and releases

- Branch from `main` (`feat/`, `fix/`, `chore/`, `perf/`, `docs/`), Conventional Commit messages and PR titles, one concern per PR. Reference issues with `Fixes #N` / `Closes #N`.
- GitHub credentials come from `GITHUB_USER` / `GITHUB_TOKEN`; never commit tokens. HTTPS automation URL: `https://x-access-token:${GITHUB_TOKEN}@github.com/<owner>/<repo>.git`.
- Release: write `openspec/specs/go-runtime/releases/go-vX.Y.Z.md` (first line `# <title>`; sections «Обновление» and «Проверки и ограничения»; state schema/protocol changes), merge to `main`, then push the tag `go-vX.Y.Z`. `.github/workflows/release.yml` runs the gates, builds reproducible archives with `scripts/release.sh` and publishes them with `SHA256SUMS`. `make release VERSION=go-vX.Y.Z` builds the same archives locally without publishing. Publishing never updates a running installation. Put the line `<!-- prerelease -->` in the notes to publish a prerelease: the updater ignores prereleases, which keeps a release that older installations cannot apply (for example a schema change before they have the migrating updater) out of their reach until it is promoted with `gh release edit <tag> --prerelease=false --latest`.
- Do not edit `CHANGELOG.md`-style files; release notes live in the files above.
