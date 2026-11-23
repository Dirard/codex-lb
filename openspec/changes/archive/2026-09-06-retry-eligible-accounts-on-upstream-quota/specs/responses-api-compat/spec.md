## MODIFIED Requirements

### Requirement: HTTP bridge model-capacity retry waits preserve stream contracts

The proxy MUST wait before replaying a pre-created HTTP bridge request with a selected-model capacity failure only when the failure happened before any downstream-visible response event and the request is still replayable as a fresh request. A hard owner MUST NOT be transferred solely because of a model-capacity message; transfer requires an explicit upstream quota-exhaustion classification and the existing account-neutral replay proof.

#### Scenario: Public propagated-error streams do not receive pre-retry keepalives

- **WHEN** a `/v1/responses`-compatible HTTP bridge stream is configured to propagate startup HTTP errors
- **AND** upstream returns a selected-model capacity error before `response.created`
- **THEN** the proxy MUST NOT emit `codex.keepalive` or account-capacity wait events before the retry completes.

#### Scenario: Replay waits remain bounded by the original bridge deadline

- **WHEN** the selected-model capacity error arrives near or after the original bridge request deadline
- **THEN** the proxy MUST NOT start a fresh upstream replay after that deadline is exhausted.

#### Scenario: Only fresh replayable bridge requests wait

- **WHEN** the selected-model capacity error belongs to an anchored request that cannot be replayed without `previous_response_id`
- **THEN** the proxy MUST forward the terminal error promptly without sleeping for the model-capacity retry delay.

#### Scenario: Retry-safe injected anchors wait only after explicit quota rejection

- **WHEN** the proxy injected `previous_response_id` and retained an account-neutral fresh request body safe to replay without that anchor
- **AND** upstream returns a selected-model capacity error explicitly classified as quota exhaustion before visible output
- **THEN** the proxy MUST apply the model-capacity wait before stripping the injected anchor and replaying the fresh request.

#### Scenario: Model capacity alone preserves a hard owner

- **WHEN** upstream returns a selected-model capacity message without explicit quota exhaustion for an owner-bound turn
- **THEN** the proxy MUST preserve the owner and normalized upstream error without cross-account replay.

#### Scenario: Remote-owner relay preserves the hidden startup wait

- **WHEN** an origin replica forwards a bridge request to its remote owner
- **THEN** the origin MUST keep its startup probe pending until the owner relay returns response headers or a terminal startup error
- **AND** a selected-model capacity wait on the owner MUST NOT cause the origin to commit HTTP 200 before that wait completes.

#### Scenario: Waiting keeps the retry tied to the pending request

- **WHEN** the proxy waits before replaying a selected-model capacity failure
- **THEN** the request MUST remain reserved in the bridge pending queue while it waits
- **AND** the proxy MUST retain the session response-create gate so a younger request cannot enter while the sole upstream reader is sleeping
- **AND** the proxy MUST release account-level and shared response-create capacity during the wait
- **AND** the proxy MUST reacquire both capacity leases before sending the replay
- **AND** the proxy MUST skip the replay if that queued request detaches before the wait completes.
