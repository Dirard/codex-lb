## ADDED Requirements

### Requirement: API key holders can fork their key

The holder of an API key that is not a fork SHALL manage forks of it with that key as the bearer under `/v1/key/forks`: `GET` lists them, `POST` creates one and returns its secret once, `GET /{id}` reads, `PATCH /{id}` changes the fields present and `DELETE /{id}` deletes. A fork, keyless local access, an internal key or a Realtime client secret SHALL get 403 `fork_management_forbidden`; an id that is not one of the caller's forks SHALL get 404 `fork_not_found`; a parent SHALL have at most 100 forks (409 `fork_limit_reached`). Forks SHALL NOT have forks and no key SHALL change its parent.

A fork SHALL never exceed its parent. When a fork is written, by its owner or the administrator, its allowed models and reasoning efforts SHALL be non-empty subsets of the parent's restrictions, its enforced model, effort and service tier SHALL match the parent's enforcement or be allowed by the parent, its expiry SHALL be no later than the parent's, and its request-pool cap and maximum wait SHALL be at most the parent's; otherwise the write SHALL fail with 422 `fork_exceeds_parent` naming the field. A fork SHALL use the parent's group, account and source scope, traffic class, transport policy and usage sections, the earlier expiry, and SHALL be served only while both keys are active and unexpired; deleting a parent SHALL delete its forks. When the administrator narrows a parent below a fork's models, efforts or tier, the fork SHALL be disabled in the same transaction.

A fork's reservation SHALL take budget on its own limits and on every active limit of its parent, and settlement, reconciliation and repricing SHALL adjust both, so fork usage counts against the parent. The parent's request pool SHALL be shared by the parent and its forks; a fork MAY have its own cap within it, and a capped fork request SHALL take a cap slot and a shared slot within the earlier maximum wait, giving the cap slot back when it gets no shared slot. Every answer to a fork's pooled request SHALL carry `x-ratelimit-fork-used-requests` (its requests in the pool during the last minute) and, with a cap, `x-ratelimit-fork-limit-requests`, with `x-ratelimit-remaining-requests` the smaller of the shared and the capped free slots.

Forks SHALL record `createdBy` and `updatedBy` (`owner` or `admin`) and `updatedAt`; management SHALL be logged with key ids only. Fork secrets SHALL be hashed in a separate domain, so code that predates forks cannot authenticate them. The dashboard SHALL list forks under their parent and link a fork to its parent.

#### Scenario: A key holder creates a narrower fork
- **WHEN** the holder of a key that allows `gpt-6-sol` and `gpt-6-luna` posts `{"name":"ci","allowedModels":["gpt-6-luna"]}` to `/v1/key/forks`
- **THEN** the answer is 201 with the fork's secret, and the fork is refused `gpt-6-sol` while it is served `gpt-6-luna` on the parent's accounts

#### Scenario: A fork wider than its parent
- **WHEN** the holder asks for a fork with a model the parent does not allow
- **THEN** the answer is 422 `fork_exceeds_parent` with `param` `allowedModels` and nothing is stored

#### Scenario: Fork usage counts on the parent
- **WHEN** a fork's request settles at 3000 microdollars and the parent has a weekly cost limit
- **THEN** both the fork's own limits and the parent's cost limit grow by 3000 microdollars, and once the parent's limit is full the fork is refused with `api_key_limit_exceeded`

#### Scenario: Forks share the parent's pool
- **WHEN** a parent with 3 requests per minute and its forks A and B send one request each
- **THEN** the third is admitted with `x-ratelimit-remaining-requests: 0`, and B's answer carries `x-ratelimit-fork-used-requests: 1`

#### Scenario: A disabled parent
- **WHEN** the administrator disables a parent key
- **THEN** its forks are refused as invalid keys until the parent is enabled again
