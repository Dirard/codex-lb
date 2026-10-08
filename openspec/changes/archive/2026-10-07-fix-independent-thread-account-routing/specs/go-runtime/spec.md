## ADDED Requirements

### Requirement: Shared prompt-cache hints do not pin independent logical threads together
New unowned requests with distinct logical Thread-Id identities SHALL NOT inherit each other's account preference solely from a shared explicit prompt-cache key. Soft cache affinity SHALL remain API-key and logical-session/thread scoped when Thread-Id is present. The configured selector SHALL choose among eligible accounts without rewriting the provider-facing cache key or weakening hard ownership.

#### Scenario: New sibling threads share a process and cache key
- **WHEN** independent unowned threads share Session_id and an explicit prompt-cache key but provide distinct Thread-Id values
- **THEN** each thread is eligible for its own account selection under the configured strategy
- **AND** neither a sibling's cache pin nor a pre-upgrade key-wide cache pin forces the new thread onto that account
- **AND** HTTP, WebSocket and compact account selection use the same scope rule

#### Scenario: A repeated thread retains its scope
- **WHEN** requests repeat the same API key, logical session/thread and cache hint
- **THEN** their valid soft binding and TTL remain reusable
- **AND** a different API key, session or thread cannot inherit that binding

#### Scenario: Existing continuations keep their owner
- **WHEN** a request has confirmed previous-response, session, turn-state or file ownership
- **THEN** changing cache hints or admitting a sibling thread does not move that owner
- **AND** existing zero-quota continuation, key/group/model authorization and quota-only failover rules remain enforced

#### Scenario: Clients without a logical Thread-Id retain compatibility
- **WHEN** a client supplies an explicit cache key without Thread-Id
- **THEN** its existing API-key-scoped cache affinity remains available independently of automatic Sticky threads
- **AND** requests without an explicit cache key keep existing automatic affinity behavior

#### Scenario: Invalid scoped hints are rejected
- **WHEN** an identity value used to scope an explicit cache hint exceeds the existing byte bound or the cache key is malformed
- **THEN** the request fails before an affinity lookup, account reservation or provider dispatch
