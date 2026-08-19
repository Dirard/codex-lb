## ADDED Requirements

### Requirement: Quota-exhausted hard-continuation owners receive bounded same-owner probes

When a request has a proven required hard-continuation owner, the service MUST allow that owner to bypass only the standard account-wide quota status after the owner's existing cooldown has expired. The request MUST remain owner-bound and MUST still satisfy retry backoff, authentication, health, activation, security, model, API-key, budget, traffic-class admission, and account-cap constraints. This exception MUST NOT apply to fresh, unowned, or soft-affinity requests. A real upstream quota response MUST remain authoritative and MUST apply the existing cooldown before another owner probe.

#### Scenario: Eligible quota-exhausted owner is probed after cooldown

- **GIVEN** a hard continuation resolves to account A
- **AND** account A has exhausted standard quota but its cooldown has expired and every other eligibility gate passes
- **WHEN** the continuation is routed
- **THEN** account A is selected for an owner-only upstream probe
- **AND** no other account is selected

#### Scenario: Active cooldown still blocks the owner probe

- **GIVEN** a hard continuation resolves to account A
- **AND** account A has an active cooldown from an upstream quota response
- **WHEN** the continuation is routed
- **THEN** the request fails through the existing owner-unavailable path without upstream dispatch
- **AND** it does not route to another account

#### Scenario: Ordinary selection still excludes quota-exhausted accounts

- **GIVEN** account A has exhausted standard quota and its reset time is still in the future
- **WHEN** a fresh, unowned, or soft-affinity request is routed
- **THEN** account A remains excluded by standard quota filtering

#### Scenario: Other owner eligibility failures are not bypassed

- **GIVEN** a hard continuation resolves to account A
- **AND** account A fails authentication, health, activation, security, model, API-key, budget, traffic-class admission, retry-backoff, or account-cap eligibility
- **WHEN** the continuation is routed
- **THEN** the request fails through the existing owner-unavailable or overload path
- **AND** it does not route to another account
