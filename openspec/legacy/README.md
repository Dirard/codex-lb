# Legacy OpenSpec material (non-normative)

This folder keeps OpenSpec artifacts written for the retired Python/Rust runtime. They are **not** requirements for the Go implementation and the OpenSpec CLI does not validate them.

- `changes/` — 188 changes that were active (unarchived) when the Go runtime replaced the Python one. All were created between 2026-07-15 and 2026-09-06, none references the Go code, and most were never synchronized into the main specs. They were moved here unchanged on 2026-10-28 instead of being archived as verified work.
- `specs/` — capabilities for mechanisms the Go contract excludes or replaces: HTTP bridge ring and multi-replica coordination, PostgreSQL/Helm/Nix delivery, install telemetry, quota phase planner, public Images API, proxy pools, Prometheus/Python observability, release-please/beta channels, Alembic migrations and the mkdocs site.

The normative product contract is [`openspec/specs/go-runtime`](../specs/go-runtime/spec.md) together with the capabilities that remain in [`openspec/specs`](../specs/). Several of those remaining capabilities still contain legacy wording; `go-runtime/context.md` explains that they are legacy references unless the Go contract cites them.

When a legacy requirement turns out to matter for Go, write a new OpenSpec change against `go-runtime` (or a new Go capability) with a test for the Go behavior. Do not move material back from this folder.
