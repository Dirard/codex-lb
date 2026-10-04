# Provider-confirmed quota recovery

## ADDED Requirements

### Requirement: Recover quota-blocked accounts after provider-side resets
Fresh usage refresh SHALL recover quota-blocked ChatGPT accounts when the backend explicitly reports `rate_limit.allowed=true` and `limit_reached=false` and all governing quota windows are complete and available. Recovery SHALL NOT require a changed reset deadline, a prior exhausted sample or a legacy block timestamp. It SHALL preserve account incarnation, newer provider outcomes and administrator policy.

#### Scenario: A provider reset keeps the same deadline
- **WHEN** a `rate_limited` or `quota_exceeded` account receives a fresh complete available snapshot with explicit backend permission
- **AND** reset deadlines remain unchanged or absent
- **THEN** the account becomes active after that snapshot is accepted
- **AND** no paid probe, reset redemption or accounting reset is performed

#### Scenario: Polls already saved the restored allowance
- **WHEN** quota rows already show zero used while the account remains blocked
- **AND** the next accepted snapshot explicitly permits usage and confirms complete available governing windows
- **THEN** that poll restores the account even without a stored refusal timestamp

#### Scenario: Percentages alone are insufficient
- **WHEN** no natural-reset proof exists and explicit permission is missing or a known governing window is missing, incomplete or exhausted
- **THEN** the account stays blocked even if another window shows 100% remaining

#### Scenario: Explicit denial overrides reset evidence
- **WHEN** a fresh snapshot explicitly reports `allowed=false` or `limit_reached=true`
- **THEN** ordinary usage refresh does not recover that account even if a reset deadline advanced

#### Scenario: Concurrent policy and newer provider outcomes win
- **WHEN** a newer provider outcome, incarnation change, pause, deactivation, reauthentication requirement or egress restriction appears during refresh
- **THEN** the stale recovery attempt cannot overwrite that state
- **AND** an active account is never made quota-blocked merely by polling
