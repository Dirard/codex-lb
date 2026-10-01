## MODIFIED Requirements

### Requirement: Preserve the legacy implementation during in-repository rewrite

The rewrite SHALL preserve the existing Git repository, required licenses and attribution. The temporary legacy source copy SHALL remain recoverable until the administrator authorizes its removal. After explicit removal approval, the active checkout and release source SHALL contain the Go implementation without requiring an in-tree Python/Rust runtime. Source cleanup MUST NOT change an installed server, delete its runtime data or expose ignored credentials/build data to version control. The Go server SHALL run as one binary without invoking Python, Rust, Node or an external codex-relay process. The supported offline legacy-data import SHALL remain implemented in Go.

#### Scenario: Modified files are relocated
- **WHEN** the legacy source tree is moved during the rewrite
- **THEN** modified and untracked implementation files retain their contents until approved removal
- **AND** `.git` and active planning instructions remain available
- **AND** credentials, databases and build caches remain excluded from commits and binary assets

#### Scenario: Administrator retires the old implementation
- **WHEN** the administrator approves removal of the temporary legacy source tree after rewrite verification
- **THEN** the old tree is removed from the active checkout recoverably and no longer belongs to the release source
- **AND** required licenses, Git history, installed data and the running service are preserved
- **AND** building and testing the Go implementation does not require the removed source directory

## ADDED Requirements

### Requirement: Key group reports include remaining purchased credit totals

When the group's upstream account-quota summary is visible, the key report SHALL also show the group's remaining Purchased credits separately from subscription usage percentages. Each non-deleted group ChatGPT account SHALL contribute once regardless of its quota-window count or membership in other groups. Known finite remaining balances SHALL be summed without treating an unknown balance as zero; negative remaining balances SHALL contribute zero available credits. An explicit zero SHALL remain distinguishable from unavailable data. Any explicit unlimited-credit account SHALL make the purchased-credit display Unlimited. The number of accounts with known balance/unlimited evidence SHALL be reported so incomplete coverage is explicit. These totals SHALL obey the same authenticated current-group scope and upstream-visibility policy as subscription summaries. Only group totals/counts SHALL be exposed, never individual provider balances or credentials. Reading the report MUST NOT purchase, redeem, reserve, settle or alter credits.

#### Scenario: Mixed known and unknown balances
- **WHEN** two group accounts have finite balances of 10.5 and 25.25, one has zero and two have no balance data
- **THEN** the report shows 35.75 purchased credits remaining with three known balances out of five accounts
- **AND** unrelated accounts and repeated window rows do not change that sum

#### Scenario: Unlimited or unavailable credits
- **WHEN** a group account explicitly has unlimited credits or all group credit balances are unknown
- **THEN** the report displays Unlimited for the former and unavailable for the latter, never an invented zero

### Requirement: Release standalone Linux binaries from the matching source

A published Go release SHALL identify its source commit and include statically linked Linux amd64 and arm64 server artifacts with the embedded dashboard, required licenses and SHA-256 checksums. Release version metadata MUST distinguish the Go version from retired legacy releases. The release SHALL retain the existing offline import and data-backup requirements. Publishing release artifacts MUST NOT implicitly update a running installation or migrate production data.

#### Scenario: Download a Go release for a VPS
- **WHEN** an operator downloads and verifies the matching Linux architecture artifact
- **THEN** the package contains the server, embedded dashboard and license texts without requiring a Python, Rust or Node runtime
- **AND** the reported version matches the release tag and checksums cover the downloadable packages
