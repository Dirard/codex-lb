## Why

The administrator approved removing the preserved legacy source and publishing the independently maintained Go implementation with Linux amd64/arm64 binaries. The existing working tree still contains the whole uncommitted rewrite and the old runtime tree.

## What Changes

- Remove `legacy/` from the repository checkout recoverably, without touching installed data or running services.
- Update active repository/distribution guidance and set the first independent Go release version.
- Commit and push the approved Go transition to `origin/fork`, then publish matching static Linux binaries, licenses and SHA-256 checksums in a GitHub release.
- Include the administrator's follow-up: show the group's aggregate remaining Purchased credits alongside subscription usage in key reports.

## Capabilities

### Modified Capabilities
- `go-runtime`: replace temporary in-tree legacy preservation with the completed Go-only distribution contract.

## Impact

Repository source layout, release metadata and downloadable artifacts. Existing Go data storage and legacy offline-import support remain unchanged. No production deployment is authorized by this release.
