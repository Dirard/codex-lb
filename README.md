# codex-lb

Independent Go implementation of the Codex account load balancer: one static server binary, SQLite and an embedded React dashboard. Download Linux amd64/arm64 packages from [GitHub releases](https://github.com/Dirard/codex-lb/releases). Production import and cutover require a separate operator decision.

No Python, Rust, Node runtime or external relay is required by the Go server. The retired implementation is retained in Git history, not in the active source tree. Linux/systemd is the delivery target, with an optional ordinary Docker image.

The runtime contract and operating notes are in [`openspec/specs/go-runtime/`](openspec/specs/go-runtime/context.md). Existing capability specifications remain the legacy behavior reference unless explicitly included by that contract.

Build with `make web-deps build`. `make check` runs vet, lint, unit and race tests, govulncheck and OpenSpec validation; `make web-check` runs the dashboard checks; `make test-web-contract` runs the dashboard API contract against a temporary installation with loopback fixtures, not real provider accounts. CI runs the same gates, and pushing a `go-vX.Y.Z` tag publishes reproducible archives built by `scripts/release.sh`. The optional load-comparison harness `scripts/compare-runtime.py` is a development tool that needs Python 3.
