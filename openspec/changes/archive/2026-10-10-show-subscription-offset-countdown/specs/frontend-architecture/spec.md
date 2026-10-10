## MODIFIED Requirements

### Requirement: Account views display subscription time remaining

The Accounts list, dashboard account cards, and dashboard account list SHALL show two subscription countdowns beside each account when `subscriptionActiveUntil` is available: time remaining to the recorded end, followed by `/` and time remaining to that timestamp plus 14 × 24 hours. The second countdown SHALL be labeled as a +14-day reference, not a confirmed extension. Both countdowns SHALL remain visible in the account layout. The presentation SHALL include a tooltip with both exact dates, following the date-display preference, and explain that the date comes from saved authorization metadata and may lag renewal. Missing or invalid dates SHALL be visibly unknown, without inventing a second date. Each past date SHALL be labeled as elapsed independently rather than definitive loss of subscription access. Subscription labels SHALL be localized through the existing translation system. The derived date SHALL NOT alter stored subscription metadata, account eligibility, routing or quotas.

#### Scenario: Future subscription date

- **WHEN** an account has a future subscription end
- **THEN** all three account surfaces show compact days, hours, or minutes countdowns to the recorded end and that end plus 14 days
- **AND** both countdowns continue updating while the view remains open even if fetched account data is unchanged
- **AND** both exact dates are available in the tooltip

#### Scenario: Unknown subscription date

- **WHEN** subscription metadata is absent or invalid
- **THEN** each account surface shows that the subscription date is unknown without a derived countdown
- **AND** it does not substitute a quota reset time or token expiration

#### Scenario: Recorded period has ended

- **WHEN** the current time reaches the recorded subscription end
- **THEN** the first subscription label indicates that the recorded period ended
- **AND** the +14-day reference continues counting down until its own timestamp is reached, then indicates that it elapsed
- **AND** existing account status, controls, and quota displays retain their behavior
