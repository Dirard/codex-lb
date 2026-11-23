# Publish the Go-only release

- Last edited with skill pack: `0.2.2`

## Title and scope

Remove the old implementation from the checkout and release `go-v1.0.0` in `Dirard/codex-lb` from `origin/fork`, with Linux amd64 and arm64 binaries.

## Planning anchor

The administrator explicitly approved legacy removal, committing and pushing the accumulated Go transition, and a GitHub release with Linux amd64/arm64 artifacts. Current local and remote `fork` both point to `003d3e501e4912e34be731e50f2d220e966b64eb`; no Go tag with the selected name exists. The active Go code and tests do not reference the old source directory. Amend the temporary legacy-preservation requirement, not the runtime migration/accounting rules.

## Connected groups or observed existing logic

- Source/layout: `legacy/` contains 2.3 GiB of old implementation, build caches, environment and preserved uncommitted work. Move the exact directory to desktop trash so recovery remains possible; do not publish it or read its private environment. Git already tracks the old root paths, whose approved deletion becomes part of the Go transition.
- Build: Makefile embeds Vite output from `internal/adapters/webui/dist` and builds with CGO disabled. Cross-build the same committed tree for Linux amd64/arm64 with version metadata and retain both license texts. Update the UI version so it does not advertise the retired Python release.
- Delivery: GitHub repository/branch are verified. Stage only reviewed source/docs, never ignored runtime data, local sessions, generated assets or binaries. Publish compressed packages and checksums with source-matching tag metadata.
- Verification: full Go tests/race/vet and 1236 frontend tests plus 10 binary contracts passed for the accumulated implementation. Repeat Go checks after source removal; verify the amd64 artifact's version/startup and contract paths. Verify arm64 build architecture/static linkage; do not claim native arm64 execution on this amd64 machine.
- Deployment/data: local service PID 733312 remains on its installed executable and external data directory. A release does not authorize restarting it, importing production data, changing networks or editing real credentials. Existing legacy offline import is retained in Go.

This bounded packaging step reuses the implementation workflow; separate reverse documentation, new release infrastructure or an automated review loop is unnecessary.

## Use cases

### 1. Retire the old source tree

verified old source directory --move exact tree to recoverable trash--> Go-only checkout --update active guidance--> independent source distribution

Implementation Logic:
Remove only `legacy/`, keep root licenses and Git history, and update active README/Go guidance so it no longer links to deleted files. Do not rewrite archived historical specs. Run tests without the old tree to verify the new runtime is independent.

### 2. Publish matching source and binaries

reviewed source --commit approved Go transition--> versioned build --check artifacts and checksums--> push fork and tag --publish GitHub assets--> verified release URLs

Implementation Logic:
Use tag `go-v1.0.0`, binary version `go-v1.0.0` and UI package version `1.0.0`. Produce static Linux amd64/arm64 tarballs with `codex-lb`, both licenses and the systemd template. Keep outputs in ignored `dist/`. Confirm the final GitHub tag points at the released commit, assets exist and server state did not change. First legacy-to-Go migration uses a separate data directory and offline import of a consistent backup, never an in-place schema conversion.

## Implementation checklist

1. [x] Retire legacy recoverably and update source/version guidance; validate specs.
2. [x] Review staged contents and secrets, commit the approved transition, and build matching release binaries.
3. [x] Verify packages and checksums, push `fork`/tag, publish and inspect the GitHub release.
4. [x] Include the requested same-group purchased-credit total and verify scoped sums, unknown/zero/unlimited balances and report rendering.

## Open questions

None blocking. Publication and commit/push are explicitly authorized. Production update is not requested. Native arm64 smoke testing is unavailable unless an emulator is already installed.

## Decision log

One new Go release line avoids pretending this is a patch of the Python distribution. Retain offline legacy-import code and historical specifications; only the preserved old implementation tree is removed. No dependency, installer, CI framework or remote deployment is introduced merely for packaging.

During release preparation the administrator also requested the remaining Purchased credits in key reports. Extend the existing group account read snapshot with persisted balance/unlimited values and aggregate each account once, before window aggregation. Report the sum of known nonnegative finite balances, a separate unlimited flag, and the count of known balances against group account count. Unknown is not zero; any unlimited account displays Unlimited. Share the existing account-pool privacy gates. The UI shows this total separately from subscription percentages; no credits are bought, redeemed or added to key budgets.

Completed on 2026-10-01. `legacy/` was moved to desktop trash, keeping its uncommitted/private files recoverable outside the repository. Source commits are `29ea3b9` (Go transition) and `714f5db` (final contract wording). Tag `go-v1.0.0` points to `714f5db27b6783104d98c889e54a7ae3fdc9d0a9`; both binaries embed that revision with `vcs.modified=false`. The release is public at https://github.com/Dirard/codex-lb/releases/tag/go-v1.0.0 and contains both architecture archives plus SHA256SUMS. Uploaded files were downloaded and checksummed successfully. Each package includes the executable, both license texts and the systemd template.

Verification after the final feature: full Go tests/race/vet, full frontend tests (1236 passed), typecheck/build, focused ESLint, and ten contract tests against the release amd64 binary all passed. Both ELF files are statically linked with CGO disabled; arm64 was cross-built, not executed natively. The current Go contract and release change validate strictly; unrelated historical legacy specs retain earlier warnings. Staged-source scanning found no private runtime paths or high-confidence secret patterns. Local service PID 733312 stayed active with zero restarts, and no installed data was changed. The final documentation-only archive commit does not change the release tag or binaries.
