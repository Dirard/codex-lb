## 1. Source cleanup

- [x] 1.1 Remove the exact legacy tree recoverably, update active guidance/version, and verify Go tests no longer need it.
- [x] 1.2 Include group Purchased credits remaining in key reports; verify aggregation, missing/zero/unlimited handling and UI with no cross-group data.

## 2. Release

- [x] 2.1 Review source/secrets and commit the approved Go transition; verify the recorded source hash.
- [x] 2.2 Build Linux amd64/arm64 packages with licenses and checksums; verify architecture, version and amd64 runtime contracts.
- [x] 2.3 Push origin/fork and the matching tag, publish the GitHub assets, and verify release metadata and unchanged local service state.
