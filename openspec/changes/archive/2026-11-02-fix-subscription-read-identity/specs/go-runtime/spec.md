## MODIFIED Requirements

### Requirement: The subscription end is read from ChatGPT in the background

The usage poller SHALL attempt to read the subscription of each ChatGPT account it polls for usage at most once per 15 minutes per account incarnation, counting failed attempts, with `GET /backend-api/subscriptions?account_id=<ChatGPT account id>`, the account's access token, the `chatgpt-account-id` header and the Codex client identity of model catalog reads: `Originator: codex_cli_rs` and `User-Agent: codex_cli_rs/<saved Codex client version>`. Without a valid saved version no read SHALL be sent. A definitive 401 SHALL be retried once after the token refresh used by usage polling. An account with a synthetic ChatGPT account id SHALL NOT be read. A successful read SHALL keep in process memory the reported `active_until` and `will_renew`, the time of the read and the ChatGPT account id it was taken for, and SHALL be served only for that account incarnation and ChatGPT account id. An answer without a valid `active_until` SHALL count as a failed read. A failed read SHALL keep the previous read and SHALL NOT change the account's status, credentials, quotas or routing, and the provider's answer MUST NOT be logged. Paused, re-login-required and deactivated accounts SHALL NOT be read.

#### Scenario: A renewal reaches the account list

- **WHEN** an account's saved ID token records an elapsed subscription end and ChatGPT reports a later `active_until`
- **THEN** after the next due read `GET /api/accounts` returns that later end as `subscription.activeUntil` with `willRenew` and `observedAt`
- **AND** `subscriptionActiveUntil` still returns the saved token's end

#### Scenario: Reads are spaced

- **WHEN** the usage poller runs every minute for an hour
- **THEN** each polled account gets at most four subscription reads, whether the reads succeed or fail

#### Scenario: A failed read changes nothing

- **WHEN** a due read fails with a transport error, a status of 400 or more, or an answer without `active_until`
- **THEN** the previous read stays in `GET /api/accounts`
- **AND** the account's status, quotas and routing are unchanged and no provider answer is logged

#### Scenario: Another incarnation does not inherit a read

- **WHEN** an account is deleted and added again, or its ChatGPT account id changes after a read
- **THEN** `GET /api/accounts` returns `subscription` null for it until its own read

#### Scenario: The read identifies itself as the Codex client

- **WHEN** a due read is sent with the saved Codex client version `0.165.4`
- **THEN** it carries `Originator: codex_cli_rs` and `User-Agent: codex_cli_rs/0.165.4`
- **AND** an upstream that refuses other clients with a 403 challenge page answers it with the subscription
