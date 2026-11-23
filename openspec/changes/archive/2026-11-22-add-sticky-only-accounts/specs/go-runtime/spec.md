## ADDED Requirements

### Requirement: A sticky-only account keeps its conversations

Every account SHALL have a sticky-only option that defaults to off and that an administrator can change through `PATCH /api/accounts/{id}` with `stickyOnly`. When the owner account of a conversation is sticky-only, Responses requests and compactions of that conversation SHALL be dispatched to the owner even if the continuation was marked as quota-refused. An explicit quota refusal from that owner SHALL reach the client as a usage-limit refusal (HTTP 429 or a WebSocket error frame with type and code `usage_limit_reached`) and SHALL NOT be replayed on another account, and the continuation SHALL NOT be marked as quota-refused. Requests without an established owner SHALL still be balanced and may still move after an explicit quota refusal before visible output. The refusal SHALL be settled and recorded as an account outcome exactly as for other accounts.

#### Scenario: A sticky-only owner runs out of quota
- **GIVEN** a conversation owned by a sticky-only account and a second eligible account
- **WHEN** the owner refuses the next turn for quota before anything is visible
- **THEN** the client receives the quota refusal and the second account is not called
- **AND** the following turn is sent to the owner again

#### Scenario: A new conversation still balances
- **WHEN** the first request of a conversation is refused for quota by a sticky-only account before anything is visible
- **THEN** the request moves to another eligible account as before
