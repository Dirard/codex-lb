## MODIFIED Requirements

### Requirement: The account list is read in a fixed number of statements

`GET /api/accounts` SHALL be built from one batch read of the non-deleted accounts with their current quotas, purchased-credit status, additional quotas, group memberships, lifetime usage totals, quota refusal times and token ciphertexts, the last 7 days of usage per account, the settings and the shell status, with a number of SQL statements independent of the number of accounts. It SHALL keep every field it returned before with the same name, type and value, and SHALL add per account `groups` (the ids of the account's groups in id order), `usage7d` (`requests`, `totalTokens` as input plus output tokens, and `costUsd` over the 7 × 24 hours ending at the read, counting the requests the reports count) and `resetCreditsObservedAt` (when the in-memory reset-credit snapshot of the account was taken, or null) and `subscription` (the account's in-memory subscription read with `activeUntil`, `willRenew` and `observedAt`, or null), and at the top level `shell` (the shell status). Usage of the last 7 days SHALL be read from the account usage rollups (UTC-day rows for whole days, hour rows at the edges) combined with raw partial hours, the unfolded tail, the journal and folded legacy hours, and while the rollups are unusable from raw events of the window, with identical results. The response SHALL decrypt only the access, refresh and ID token ciphertexts whose state and expiry it returns, SHALL NOT read or decrypt external API keys, SHALL send `Server-Timing` and MUST NOT call a provider.

#### Scenario: Statement count does not grow with the installation

- **WHEN** `GET /api/accounts` is served for 9 and for 100 accounts
- **THEN** both reads execute the same number of SQL statements

#### Scenario: Existing fields are unchanged

- **WHEN** an account has quotas in every window, an expired primary window, purchased credits, additional quotas, a quota refusal, tokens with and without expiry, an unreadable token, no stored credential, an alias or none, and a reset-credit snapshot
- **THEN** every field the per-account reads produced is present with the same value, and `groups`, `usage7d`, `resetCreditsObservedAt`, `subscription` and `shell` are added

#### Scenario: Rollups and raw events agree

- **WHEN** the last 7 days are read while the rollups are folded, the folder stopped at a random cursor, or journal rows are pending
- **THEN** every account's `usage7d` equals the evaluation of the raw events and folded legacy hours of the same window

#### Scenario: No provider request

- **WHEN** `GET /api/accounts` is served
- **THEN** no request reaches the ChatGPT usage, reset-credit, subscription or token endpoints

## ADDED Requirements

### Requirement: The subscription end is read from ChatGPT in the background

The usage poller SHALL attempt to read the subscription of each ChatGPT account it polls for usage at most once per 15 minutes per account incarnation, counting failed attempts, with `GET /backend-api/subscriptions?account_id=<ChatGPT account id>`, the account's access token and the `chatgpt-account-id` header. A definitive 401 SHALL be retried once after the token refresh used by usage polling. An account with a synthetic ChatGPT account id SHALL NOT be read. A successful read SHALL keep in process memory the reported `active_until` and `will_renew`, the time of the read and the ChatGPT account id it was taken for, and SHALL be served only for that account incarnation and ChatGPT account id. An answer without a valid `active_until` SHALL count as a failed read. A failed read SHALL keep the previous read and SHALL NOT change the account's status, credentials, quotas or routing, and the provider's answer MUST NOT be logged. Paused, re-login-required and deactivated accounts SHALL NOT be read.

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
