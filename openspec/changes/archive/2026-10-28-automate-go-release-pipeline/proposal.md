## Why

Go releases go-v1.0.0…go-v1.0.10 were built by hand on one workstation with Go 1.26.6, which ships 11 reachable standard-library vulnerabilities. No automated gate runs tests, race tests, vet, lint or vulnerability scans before a release, and a clean checkout does not compile without a prior dashboard build.

## What Changes

- Pin the patched toolchain (`toolchain go1.26.10`) and tidy module requirements.
- Add `make check` / `make web-check` gates and a GitHub Actions CI workflow that runs them for pull requests and `main`.
- Build releases from a pushed `go-vX.Y.Z` tag in CI with `scripts/release.sh`, producing deterministic archives in the existing layout plus `SHA256SUMS`, and publish notes from `openspec/specs/go-runtime/releases/<tag>.md`.
- Keep a tracked embed placeholder so a clean checkout compiles; the UI still answers 503 until assets are built.

## Capabilities

### Modified Capabilities

- `go-runtime`: release build, verification and publication contract.

## Impact

Build tooling, CI workflows, `go.mod`/`go.sum`, Dockerfile Go image and documentation. Runtime behavior, schema (33), update protocol (1) and archive layout are unchanged. Publishing still never updates a running installation.
