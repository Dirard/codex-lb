## ADDED Requirements

### Requirement: Changes and releases pass automated gates on a patched toolchain

The module SHALL pin a Go toolchain without known reachable standard-library vulnerabilities. Pull requests and `main` SHALL run vet, lint, unit tests, race tests, vulnerability scanning, dashboard lint/typecheck/tests and OpenSpec validation without provider credentials. A pushed `go-vX.Y.Z` tag SHALL build the dashboard and the Linux amd64/arm64 binaries in CI, rerun the gates, and publish deterministic archives whose layout is accepted by the runtime updater together with `SHA256SUMS` and the release notes file for that tag. Building the same commit twice MUST produce identical checksums. A clean checkout without built dashboard assets SHALL compile and pass tests.

#### Scenario: A release tag is pushed
- **WHEN** an operator pushes `go-v1.0.11` after merging its release notes
- **THEN** CI publishes `codex-lb-go-v1.0.11-linux-amd64.tar.gz`, `codex-lb-go-v1.0.11-linux-arm64.tar.gz` and `SHA256SUMS` only after the gates pass
- **AND** each archive contains exactly `codex-lb`, `LICENSE`, `LICENSE.codex-relay` and `codex-lb.service`
- **AND** no running installation is updated by publication

#### Scenario: Reproducing a release locally
- **WHEN** a maintainer runs `scripts/release.sh go-v1.0.11 <dir>` on the tagged commit
- **THEN** the produced `SHA256SUMS` equals the published one
