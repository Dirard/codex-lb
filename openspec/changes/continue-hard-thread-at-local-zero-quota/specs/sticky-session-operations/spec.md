## MODIFIED Requirements

### Requirement: Quota-exhausted hard-continuation owners receive bounded same-owner probes

When a request has a proven required hard-continuation owner, including an owner-bound reconnect or reattach of an existing HTTP bridge thread, the service MUST allow that owner to bypass only the locally stored standard account-wide quota status after the owner's existing cooldown has expired. The request MUST remain owner-bound and MUST still satisfy retry backoff, authentication, health, activation, security, model, API-key, budget, traffic-class admission, and account-cap constraints. This exception MUST NOT apply to fresh, unowned, or soft-affinity requests. The existing thread's normal user request MUST be the only upstream attempt; the service MUST NOT send a separate synthetic availability probe. A real upstream rejection MUST remain authoritative, MUST follow the existing client-error and cooldown behavior, and MUST NOT leave the rejected request holding bridge creation while local selection is retried indefinitely.

#### Scenario: Eligible quota-exhausted owner is probed after cooldown

- **GIVEN** a hard continuation resolves to account A
- **AND** account A has exhausted standard quota but its cooldown has expired and every other eligibility gate passes
- **WHEN** the continuation is routed
- **THEN** account A is selected for the existing request's owner-only upstream attempt
- **AND** no other account is selected
- **AND** no separate availability request is sent

#### Scenario: Hard HTTP bridge reconnect keeps owner quota bypass

- **GIVEN** an existing hard HTTP bridge thread is owned by account A
- **AND** local standard quota state for account A is exhausted while its upstream cooldown is not active
- **WHEN** the bridge reconnects before sending the thread's next normal request
- **THEN** reconnect selection treats account A as the required continuity owner
- **AND** the normal request is sent to account A without a local quota rejection

#### Scenario: Active cooldown still blocks the owner probe

- **GIVEN** a hard continuation resolves to account A
- **AND** account A has an active cooldown from an upstream quota response
- **WHEN** the continuation is routed
- **THEN** the request fails through the existing owner-unavailable path without upstream dispatch
- **AND** it does not route to another account

#### Scenario: Upstream rejection ends the current request

- **GIVEN** an existing hard continuation was admitted to its owner despite local zero quota
- **WHEN** ChatGPT API rejects that normal request
- **THEN** the service returns the classified rejection through its existing client error path
- **AND** applies the existing account cooldown and settlement behavior
- **AND** does not keep the rejected request in an HTTP bridge creation wait

#### Scenario: Ordinary selection still excludes quota-exhausted accounts

- **GIVEN** account A has exhausted standard quota and its reset time is still in the future
- **AND** account B is available
- **WHEN** a fresh, unowned, or soft-affinity request is routed
- **THEN** account A remains excluded by standard quota filtering
- **AND** account B is selected

#### Scenario: New thread fails when no account is available

- **GIVEN** every account available to a fresh thread has exhausted standard quota
- **WHEN** the fresh thread is routed
- **THEN** no exhausted account receives the request
- **AND** the client receives the existing no-account or quota error

#### Scenario: Other owner eligibility failures are not bypassed

- **GIVEN** a hard continuation resolves to account A
- **AND** account A fails authentication, health, activation, security, model, API-key, budget, traffic-class admission, retry-backoff, or account-cap eligibility
- **WHEN** the continuation is routed
- **THEN** the request fails through the existing owner-unavailable or overload path
- **AND** it does not route to another account
