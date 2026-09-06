## ADDED Requirements

### Requirement: Account views display subscription time remaining

The Accounts list, dashboard account cards, and dashboard account list SHALL show subscription time remaining beside each account when `subscriptionActiveUntil` is available. The presentation SHALL include a tooltip with the exact recorded date, following the date-display preference, and explain that the date comes from saved authorization metadata and may lag renewal. Missing or invalid dates SHALL be visibly unknown. A past date SHALL be labeled as an elapsed recorded period rather than definitive loss of subscription access. Subscription labels SHALL be localized through the existing translation system.

#### Scenario: Future subscription date

- **WHEN** an account has a future subscription end
- **THEN** all three account surfaces show a compact days, hours, or minutes countdown
- **AND** the countdown continues updating while the view remains open even if fetched account data is unchanged
- **AND** the recorded end date is available in the tooltip

#### Scenario: Unknown subscription date

- **WHEN** subscription metadata is absent or invalid
- **THEN** each account surface shows that the subscription date is unknown
- **AND** it does not substitute a quota reset time or token expiration

#### Scenario: Recorded period has ended

- **WHEN** the current time reaches the recorded subscription end
- **THEN** the subscription label changes to indicate that the recorded period ended
- **AND** existing account status, controls, and quota displays retain their behavior
