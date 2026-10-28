## 1. Toolchain and gates

- [x] 1.1 Pin `toolchain go1.26.10`, tidy modules, update x/crypto and x/sys; govulncheck reports no reachable vulnerability.
- [x] 1.2 Add `.golangci.yml`, fix its actionable findings, and add `make check`, `make web-check`, `make release`.
- [x] 1.3 Track `internal/adapters/webui/dist/.gitkeep` so a clean checkout compiles.

## 2. CI and release

- [x] 2.1 Add `.github/workflows/ci.yml` (Go, dashboard and OpenSpec jobs).
- [x] 2.2 Add `scripts/release.sh` and `.github/workflows/release.yml`; verify two local builds produce identical `SHA256SUMS` and that the updater extractor accepts the archive.
- [x] 2.3 Verify CI is green on the pull request, then sync the delta into `openspec/specs/go-runtime/spec.md` and archive this change.
