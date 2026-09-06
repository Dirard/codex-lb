## MODIFIED Requirements

### Requirement: Cached caps govern runtime admission

New account selection, account lease acquisition, opportunistic admission, account-cap error reporting, and warm HTTP bridge stream-lease reacquisition MUST use one dashboard-settings cache snapshot obtained before entering runtime locks. These paths MUST NOT read the database or await the dashboard settings cache while holding a runtime lock.

#### Scenario: Dashboard value overrides startup environment

- **GIVEN** the process environment stream cap differs from the persisted dashboard stream cap
- **WHEN** a new stream selection or lease acquisition occurs
- **THEN** the persisted cached dashboard cap controls the decision

#### Scenario: Warm bridge reacquisition honors unlimited dashboard cap

- **GIVEN** the process startup stream cap is positive
- **AND** the persisted cached dashboard stream cap is `0`
- **AND** a warm idle HTTP bridge session must reacquire its stream lease for a new turn
- **WHEN** the startup-default cap would otherwise reject that lease
- **THEN** reacquisition treats the dashboard cap as unlimited
- **AND** it does not return `account_stream_cap`
