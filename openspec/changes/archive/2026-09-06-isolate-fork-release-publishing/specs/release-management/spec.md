## ADDED Requirements

### Requirement: Fork release tags are isolated from upstream publishing

For a release tag prefixed with `fork-`, the inherited Release workflow MUST skip upstream metadata resolution and dependent PyPI, Docker, and Helm publication. It MUST NOT withdraw that manually managed fork release solely because upstream artifact jobs were skipped. Non-fork stable and beta tags MUST retain the existing pipeline.

#### Scenario: Operator publishes a fork release

- **WHEN** `fork-2026.09.06.1` is published or supplied to workflow dispatch
- **THEN** upstream artifact jobs are skipped and the manually managed release is retained

#### Scenario: Upstream-compatible release is published

- **WHEN** a stable or beta `v*` release is published
- **THEN** the existing metadata, publishing, and failure-withdrawal rules apply
