## ADDED Requirements

### Requirement: Recoverable upstream quota errors switch accounts before reaching the client

When OpenAI explicitly rejects an account for quota exhaustion before client-visible output, an existing verified account-neutral full replay and an eligible replacement account SHALL cause the proxy to retry internally within the existing request deadline and retry budget before emitting a terminal quota error downstream. A client-supplied `previous_response_id` MUST NOT prevent this recovery solely because the identifier was not injected by the proxy. This requirement SHALL apply to explicit quota responses received during upstream connection establishment as well as upstream response events whenever the same replay proof is available. The proxy SHALL retain the existing account-assignment, model, file-ownership, replay-safety, and usage-settlement restrictions.

#### Scenario: Client-owned chain has a verified full resend

- **GIVEN** a request with a client-supplied previous-response identifier is owned by account A
- **AND** the proxy has verified an account-neutral full resend for that request
- **WHEN** OpenAI rejects A for quota exhaustion before client-visible output and account B is eligible
- **THEN** the proxy retries on B with the verified full input and without A's previous-response or turn-state identifiers
- **AND** the client receives B's successful response without A's terminal quota error
- **AND** the retry excludes A and preserves final usage settlement

#### Scenario: Persisted hard affinity cannot reselect the rejected owner

- **GIVEN** a durable hard mapping still identifies account A
- **WHEN** an actual quota rejection authorizes a verified account-neutral replay
- **THEN** replacement selection omits the old hard routing anchor and excludes A
- **AND** it does not overwrite the old mapping or send its turn-state token to B
- **AND** other account-bound requests retain their ownership checks

#### Scenario: Non-quota errors do not authorize owner transfer

- **WHEN** an owner-bound request encounters generic rate limiting, authentication failure, model rejection, or a transport failure without explicit upstream quota evidence
- **THEN** the new quota-recovery path is not used
- **AND** existing error handling and owner affinity remain in effect

#### Scenario: Quota recovery is unsafe or unavailable

- **WHEN** no eligible replacement exists, the full replay cannot be verified as account-neutral, or client-visible output prevents safe replay
- **THEN** the proxy preserves its existing terminal error and settlement behavior
- **AND** it does not discard ownership restrictions or duplicate output to hide the failure

#### Scenario: Local zero-percent usage alone preserves an active owner

- **WHEN** local usage reports zero remaining quota for an active thread's owner without an upstream quota rejection
- **THEN** this recovery path does not transfer the request to another account

#### Scenario: Account-health writes wait for keyed settlement

- **WHEN** a keyed request retries after account A's quota rejection
- **THEN** A's classified health failure is deferred until reservation settlement or fallback release commits
- **AND** a later failure or success on account B is accounted for independently
- **AND** restoring A's terminal error after a failed replacement attributes that error to A while preserving B's socket ownership until retirement
