## MODIFIED Requirements

### Requirement: Account views display subscription time remaining

The Accounts list SHALL show two subscription countdowns beside each account when a subscription end is available: time remaining to the end, followed by `/` and time remaining to that timestamp plus 14 × 24 hours. The end SHALL be `subscription.activeUntil`, the end ChatGPT reported on the last background read, when the account has one, and otherwise `subscriptionActiveUntil` from saved authorization metadata. The second countdown SHALL be labeled as a +14-day reference, not a confirmed extension. Both countdowns SHALL remain visible in the account layout. The presentation SHALL include a tooltip with both exact dates, following the date-display preference, and its source: for a ChatGPT read, the time of the read and whether the subscription renews; for saved authorization metadata, that the date may lag renewal. Missing or invalid dates SHALL be visibly unknown, without inventing a second date. A past end SHALL read "Subscription ended" only when it comes from a ChatGPT read taken at or after that end; any other past end SHALL be labeled as an elapsed recorded period rather than definitive loss of subscription access, and the +14-day reference SHALL be labeled as elapsed independently. Subscription labels SHALL be localized through the existing translation system. The derived date SHALL NOT alter stored subscription metadata, account eligibility, routing or quotas.

#### Scenario: Future subscription date

- **WHEN** an account has a future subscription end
- **THEN** the Accounts list shows compact days, hours, or minutes countdowns to the end and that end plus 14 days
- **AND** both countdowns continue updating while the view remains open even if fetched account data is unchanged
- **AND** both exact dates are available in the tooltip

#### Scenario: ChatGPT reports a renewal the saved metadata lacks

- **WHEN** saved authorization metadata records an elapsed end and the account's ChatGPT read reports a later end
- **THEN** both countdowns follow the end ChatGPT reported
- **AND** the tooltip shows when that read was taken and whether the subscription renews

#### Scenario: Unknown subscription date

- **WHEN** the account has no ChatGPT read and its subscription metadata is absent or invalid
- **THEN** the Accounts list shows that the subscription date is unknown without a derived countdown
- **AND** it does not substitute a quota reset time or token expiration

#### Scenario: Recorded period has ended

- **WHEN** the current time reaches an end from saved metadata, or an end from a ChatGPT read taken before it
- **THEN** the first subscription label indicates that the recorded period ended
- **AND** the +14-day reference continues counting down until its own timestamp is reached, then indicates that it elapsed
- **AND** existing account status, controls, and quota displays retain their behavior

#### Scenario: ChatGPT confirms the end

- **WHEN** a ChatGPT read taken after its own end still reports that end
- **THEN** the first subscription label says that the subscription ended
