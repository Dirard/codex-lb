## ADDED Requirements

### Requirement: Subscription compaction uses the in-band Responses contract

Standalone and HTTP-triggered subscription compaction SHALL post to the private `/responses` endpoint with `store=false`, `stream=true` and one terminal `compaction_trigger`, preserving existing owner, policy, session and billing constraints. The adapter SHALL accept bounded valid SSE with an optional absent Content-Type and the original-compatible JSON response form. It SHALL collect terminal output or reconstruct missing terminal output from indexed item events and unindexed done items. A successful compact SHALL expose a `response.compaction`-compatible envelope containing the encrypted compact item, using explicit compaction items before the last message-shaped encrypted summary. Missing response IDs alone SHALL NOT invalidate compaction. Actual usage and service tier MUST survive output normalization and error classification.

#### Scenario: Upstream returns a streamed compact result
- **WHEN** upstream completes an in-band compact operation
- **THEN** public compact and trigger callers receive a usable compact item with actual accounted usage
- **AND** the old private `/responses/compact` route is never used

#### Scenario: Upstream omits the terminal output array
- **WHEN** completed output items arrived before a valid terminal response
- **THEN** the adapter reconstructs the compact output in index order followed by unindexed done items, within bounded memory

#### Scenario: An operation fails after upstream output
- **WHEN** a compact stream has emitted output and then fails or disconnects
- **THEN** no authentication or quota replay duplicates that operation
- **AND** valid reported usage settles once while unknown usage remains reserved for reconciliation

#### Scenario: Compaction holds account admission
- **WHEN** the first valid upstream event arrives
- **THEN** create capacity is released while stream capacity remains owned until attempt completion or cancellation
