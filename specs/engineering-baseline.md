# Engineering baseline: patched toolchain, one-command checks, CI and reproducible releases

- Last edited with skill pack: `0.2.2`

## Title and scope

Make every codex-lb change and release pass the same automated gate on a patched Go toolchain, and publish releases from a clean checkout instead of a developer machine. The process documents (`AGENTS.md`, `.agents/`, OpenSpec layout) are brought in line with the Go-only repository so contributors follow gates that actually exist.

## Planning anchor

- Entrypoints: `go.mod`, `Makefile`, the missing `.github/workflows/`, `internal/adapters/webui/webui.go#embedded` (`//go:embed all:dist`), `AGENTS.md`, `.agents/`, `openspec/changes/*`, `openspec/specs/*`.
- Changed assumptions:
  - Releases go-v1.0.0…go-v1.0.10 were built by hand with go1.26.6; govulncheck reports 11 reachable stdlib vulnerabilities fixed in go1.26.9/go1.26.10 (`.local/audit/03-security.md` SEC-12, `.local/audit/07-quality-hygiene.md` QA-1).
  - No CI exists; `AGENTS.md` describes CodeRabbit, `.github/CONTRIBUTING.md`, `uv run pytest`, Alembic and `docs/`, none of which exist (QA-2, QA-3).
  - A clean checkout does not compile because `internal/adapters/webui/dist` is ignored and `all:dist` matches nothing (`.local/audit/05-ops-runtime.md` OPS-8).
  - 188 active OpenSpec changes and 25 capabilities describe the retired Python runtime (QA-4, QA-5, OPS-20).
- Non-local: touches build, CI, release publication, repository docs and OpenSpec layout, but no runtime behavior.
- Impacted specs in `specs/`: none besides this one (`specs/spec-lifecycle/` is lifecycle policy).
- Impacted OpenSpec capabilities: `go-runtime` (release requirement already demands checksums and matching source; unchanged) — action `reuse`; legacy capabilities — action `archive` (moved under `openspec/legacy/`).

## Connected groups or observed existing logic

### Observed Existing Logic

1. Build: `Makefile#build` runs the web build into `internal/adapters/webui/dist` then `CGO_ENABLED=0 go build -trimpath -ldflags '-s -w -X main.version=$(VERSION)'`. The Go binary is bit-reproducible (OPS-8 measurement).
2. Release layout consumed by the self-updater: `internal/adapters/releases` downloads `codex-lb-<tag>-linux-<arch>.tar.gz` and `SHA256SUMS` from GitHub releases; `extract.go#extractBinary` accepts only `codex-lb` plus known regular auxiliary files (`LICENSE`, `LICENSE.codex-relay`, `codex-lb.service`).
3. Published notes are Russian Markdown with sections «Обновление», «Проверки и ограничения» and a contract link (`gh release view go-v1.0.10`).
4. Frontend: `npm --prefix web run install:deps` (bun 1.3.14 via npx, frozen lockfile), `lint`, `typecheck`, `test` (vitest), `build`.
5. OpenSpec CLI is not installed; `npx @fission-ai/openspec@1.12.0` from cache validates 61/61 specs.
6. `.agents/settings.json` points hooks at a non-existent `.claude/hooks/*.py`; `.agents/environments/environment.toml` runs `uv sync` and `cd frontend`; `.agents/scheduled_tasks.lock` (pid/session id) is tracked; `.gitattributes` refers to `frontend/bun.nix`.

## Use cases

### 1. Build on a patched toolchain with tidy modules
go.mod with go 1.26.0 and indirect-only requirements --pin toolchain go1.26.10 and tidy modules--> patched reproducible build --govulncheck--> no reachable stdlib vulnerability

Requirements:

  R1:
The module SHALL declare `toolchain go1.26.10` (or newer patch) so builds and CI use a toolchain without the reported stdlib vulnerabilities.

  R2:
`go mod tidy -diff` SHALL be empty; direct dependencies SHALL NOT be marked `// indirect`.

  R3:
The Dockerfile build stage SHALL use the same patched Go minor/patch line.

Files And Functions:
 - existing: go.mod - toolchain line, tidy
 - existing: go.sum - tidy
 - existing: Dockerfile - `golang:1.26.10-alpine`

Validation:
`govulncheck ./...` reports no reachable vulnerability in called stdlib symbols; `go version -m bin/codex-lb` shows go1.26.10.

### 2. Run every gate with one command
developer checkout --make check--> (go vet, go test, go test -race, golangci-lint, govulncheck, OpenSpec validation, web lint/typecheck/test) --stop at the first failing gate--> pass or first failing gate

Requirements:

  R4:
`make check` SHALL run vet, unit tests, race tests, lint with the repository `.golangci.yml`, vulnerability scan and OpenSpec validation; `make web-check` SHALL run frontend lint, typecheck and tests.

  R5:
The lint configuration SHALL enable only linters whose findings are actionable (govet, staticcheck, unused, ineffassign, errorlint, bodyclose, noctx, gocritic subset) so the gate passes on a clean tree after the real findings are fixed.

  R6:
A clean checkout without built web assets SHALL compile and pass `go test ./...`; the UI handler keeps answering 503 "assets are not built" when `index.html` is absent.

Files And Functions:
 - existing: Makefile - new `check`, `lint`, `vuln`, `spec-validate`, `web-check`, `release` targets
 - planned: .golangci.yml - actionable linter set
 - planned: internal/adapters/webui/dist/.gitkeep - tracked placeholder so `all:dist` embeds at least one file
 - existing: .gitignore - keep ignoring built assets but not the placeholder
 - existing: internal/adapters/webui/webui.go#serveIndex - unchanged 503 fallback

Tests:
 - description: clean checkout builds
   input: `git clone` without `web` build
   workflow: clean checkout --go vet ./... and go test ./...--> success
   expected outcome: no `pattern all:dist: no matching files found`

### 3. Gate pull requests and main in GitHub Actions
push or pull request to main --CI workflow--> (Go job: vet, test, race, lint, vuln; web job: install, lint, typecheck, test, build; spec job: openspec validate) --report the combined result--> required status

Requirements:

  R7:
CI SHALL run on pull requests and pushes to `main` with read-only token permissions and pinned tool versions.

  R8:
CI SHALL NOT use provider credentials or call real providers.

Files And Functions:
 - planned: .github/workflows/ci.yml - Go, web and spec jobs

### 4. Publish a release from a tag
pushed tag go-vX.Y.Z --release workflow--> clean build of web assets --build linux amd64/arm64 static binaries with version from tag--> deterministic tar.gz archives with LICENSE, LICENSE.codex-relay, codex-lb.service --SHA256SUMS--> GitHub release with notes file

Requirements:

  R9:
Release archives SHALL keep the layout accepted by `releases.extractBinary` and the names `codex-lb-<tag>-linux-<arch>.tar.gz`.

  R10:
Archives SHALL be deterministic (sorted entries, numeric owner 0, mtime of the tagged commit, `gzip -n`) so two builds of a tag produce identical `SHA256SUMS`.

  R11:
The workflow SHALL run the same gates as CI before uploading and SHALL take the release notes from `openspec/specs/go-runtime/releases/<tag>.md` when present.

  R12:
`scripts/release.sh` SHALL produce the same artifacts locally for verification without publishing.

Files And Functions:
 - planned: scripts/release.sh - build and package for one tag into an output directory
 - planned: .github/workflows/release.yml - tag-triggered build, verify, publish
 - existing: deploy/codex-lb.service - packaged unit template

Tests:
 - description: reproducible packaging
   input: same tag built twice into different directories
   workflow: tag --scripts/release.sh twice--> two SHA256SUMS
   expected outcome: identical checksums; `tar tzf` lists exactly the four files
 - description: updater accepts the archive
   input: archive produced by `scripts/release.sh`
   workflow: archive --releases.extractBinary--> extracted codex-lb
   expected outcome: no unexpected-entry error

### 5. Process documents describe the Go repository
outdated AGENTS.md, .agents tooling and legacy OpenSpec tree --rewrite and relocate--> contributor guidance that matches the repository

Requirements:

  R13:
`AGENTS.md` SHALL describe only existing gates (`make check`, `make web-check`, CI), the Go layering rules, OpenSpec usage, solution specs in `specs/` and the release procedure; references to absent artifacts SHALL be removed.

  R14:
Inoperative agent hooks, the Python-era environment file, the bridge handover and the tracked lock file SHALL be removed; the lock file SHALL be ignored.

  R15:
Active OpenSpec changes created for the retired Python runtime and capabilities for features excluded by `go-runtime` SHALL move to `openspec/legacy/` with a README that states they are non-normative; the remaining capabilities stay in `openspec/specs/` and `openspec validate --specs` passes.

  R16:
`.gitattributes` SHALL not reference removed paths.

Files And Functions:
 - existing: AGENTS.md - rewrite
 - existing: .agents/skills/project-conventions/conventions.md - Go-only rules
 - existing: .agents/hooks/*, .agents/settings.json, .agents/skills/skill-rules.json, .agents/environments/environment.toml, .agents/handover/http-bridge-keepalive-hang.md, .agents/scheduled_tasks.lock - remove
 - planned: openspec/legacy/README.md - status of relocated material
 - existing: .gitattributes - drop `frontend/bun.nix`

## Implementation checklist

1. [ ] Pin `toolchain go1.26.10`, run `go mod tidy`, update `golang.org/x/crypto` and `golang.org/x/sys` patch versions, Dockerfile Go image (R1, R2, R3).
2. [ ] Add tracked `internal/adapters/webui/dist/.gitkeep` and `.gitignore` exception (R6).
3. [ ] Add `.golangci.yml`, fix the actionable findings it reports (dead code, `errors.Is`, ignored `ConfigureAccounts` error, `noctx` in production code) (R5).
4. [ ] Extend `Makefile` with `check`, `lint`, `vuln`, `spec-validate`, `web-check`, `release` (R4).
5. [ ] Add `scripts/release.sh` and verify reproducibility and updater acceptance locally (R9, R10, R12).
6. [ ] Add `.github/workflows/ci.yml` and `.github/workflows/release.yml` (R7, R8, R11).
7. [ ] Rewrite `AGENTS.md`, clean `.agents/`, fix `.gitattributes` (R13, R14, R16).
8. [ ] Relocate legacy OpenSpec changes/capabilities to `openspec/legacy/` with README; run `openspec validate --specs` (R15).
9. [ ] Run `make check` and `make web-check`; record results in the decision log.

## Open questions

None blocking.

## Decision log

- 2026-10-28: The administrator made the agent product owner with authority to change the product, push and publish releases. CI and release workflows are therefore added without a separate approval.
- 2026-10-28: Legacy OpenSpec material is relocated, not deleted, so links from archived Go changes remain resolvable by path search and history stays browsable without `git log`.
- 2026-10-28: Solution specs in `specs/` plan implementation slices; OpenSpec `openspec/specs/` stays the normative product contract. `AGENTS.md` documents this split.
- 2026-10-28: `-race` stays in CI despite ~9 minutes because the proxy relies on concurrent ownership and settlement invariants.
- 2026-10-28: The CI Go job builds the dashboard first: `TestRuntimeServesEmbeddedUIAndDrains` verifies the embedded UI exactly as released, so a clean checkout compiles (R6) but that runtime test needs built assets.
