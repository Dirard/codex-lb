## ADDED Requirements

### Requirement: The account list is read in a fixed number of statements

`GET /api/accounts` SHALL be built from one batch read of the non-deleted accounts with their current quotas, purchased-credit status, additional quotas, group memberships, lifetime usage totals, quota refusal times and token ciphertexts, the last 7 days of usage per account, the settings and the shell status, with a number of SQL statements independent of the number of accounts. It SHALL keep every field it returned before with the same name, type and value, and SHALL add per account `groups` (the ids of the account's groups in id order), `usage7d` (`requests`, `totalTokens` as input plus output tokens, and `costUsd` over the 7 × 24 hours ending at the read, counting the requests the reports count) and `resetCreditsObservedAt` (when the in-memory reset-credit snapshot of the account was taken, or null), and at the top level `shell` (the shell status). Usage of the last 7 days SHALL be read from the account usage rollups (UTC-day rows for whole days, hour rows at the edges) combined with raw partial hours, the unfolded tail, the journal and folded legacy hours, and while the rollups are unusable from raw events of the window, with identical results. The response SHALL decrypt only the access, refresh and ID token ciphertexts whose state and expiry it returns, SHALL NOT read or decrypt external API keys, SHALL send `Server-Timing` and MUST NOT call a provider.

#### Scenario: Statement count does not grow with the installation

- **WHEN** `GET /api/accounts` is served for 9 and for 100 accounts
- **THEN** both reads execute the same number of SQL statements

#### Scenario: Existing fields are unchanged

- **WHEN** an account has quotas in every window, an expired primary window, purchased credits, additional quotas, a quota refusal, tokens with and without expiry, an unreadable token, no stored credential, an alias or none, and a reset-credit snapshot
- **THEN** every field the per-account reads produced is present with the same value, and `groups`, `usage7d`, `resetCreditsObservedAt` and `shell` are added

#### Scenario: Rollups and raw events agree

- **WHEN** the last 7 days are read while the rollups are folded, the folder stopped at a random cursor, or journal rows are pending
- **THEN** every account's `usage7d` equals the evaluation of the raw events and folded legacy hours of the same window

#### Scenario: No provider request

- **WHEN** `GET /api/accounts` is served
- **THEN** no request reaches the ChatGPT usage, reset-credit or token endpoints

### Requirement: The Accounts page manages accounts from one table

The Accounts page SHALL show every account in one table with a toolbar: search by alias, e-mail or account id (case-insensitive), a status filter, a group filter (every group and Ungrouped), a sort select (status severity by default, next reset soonest or latest, 5 h remaining, weekly remaining, most reset credits, name ascending or descending) and Add account, which opens the OAuth or import chooser. Each row SHALL show the label (alias, else e-mail) with the e-mail, the plan with the subscription countdowns, the status with its reason, the group names, the 5 h, weekly and (when any account has one) monthly remaining with their reset times, purchased credits, the reset credits of the in-memory snapshot, the warm-up state, an inline priority select (burn first, normal, preserve), inline Re-login for accounts needing re-login or deactivated and Pause or Resume for the others, and a menu with Probe, Export auth, Redeem reset credit (when the snapshot has credits) and Delete. Status severity SHALL order re-login required, deactivated, quota exceeded, rate limited, paused, then active. Accounts needing operator recovery (re-login required or deactivated) SHALL NOT offer the priority select, Pause or Resume, and Probe SHALL be disabled for them and for paused accounts. Opening a row SHALL show the account's detail sheet; `/accounts?selected=<id>` SHALL open it directly. The page SHALL poll only `GET /api/accounts`, every 30 seconds while visible, and SHALL take the shell status from that response; while loading it SHALL show a table skeleton, on an error a retryable inline error that keeps the previous rows, and without accounts "No accounts" with Add account.

#### Scenario: Problems sort first

- **WHEN** the Accounts page opens with an active, a paused, a rate-limited and a re-login-required account
- **THEN** the re-login-required account is the first row and the active account the last

#### Scenario: Filter by group

- **WHEN** the operator selects a group in the group filter
- **THEN** only the accounts in that group are listed, and Ungrouped lists the accounts without a group

#### Scenario: One poll

- **WHEN** the operator keeps the Accounts page open
- **THEN** the only periodic request is `GET /api/accounts`, whose `shell` updates the header badge and the status bar

### Requirement: Browsing accounts never calls a provider

Opening the Accounts page or an account's detail sheet SHALL NOT issue a request that calls a provider. The detail sheet SHALL show the reset credits of the in-memory snapshot as "available N as of HH:MM" with the snapshot time and SHALL request `GET /api/accounts/{id}/usage-reset-credits` only when the operator presses Check now; provider refreshes SHALL otherwise happen only on Probe and Re-login. The 7-day quota chart (`GET /api/accounts/{id}/trends`) SHALL be requested only while the sheet is open and refetched every 5 minutes while it stays open.

#### Scenario: Selection does not refresh from the provider

- **WHEN** the operator opens the Accounts page and then two accounts' detail sheets
- **THEN** no `usage-reset-credits` request is made

#### Scenario: Check now

- **WHEN** the operator presses Check now in a detail sheet
- **THEN** exactly one `GET /api/accounts/{id}/usage-reset-credits` is requested and its count is shown with the time of the check

### Requirement: Account usage figures carry their period

Every usage figure on the Accounts page SHALL carry an explicit period label: request, token and cost figures SHALL be labelled "Last 7 days" (from `usage7d`) or "Lifetime" (from `requestUsage`), and quota figures SHALL name their window with its reset time.

#### Scenario: Usage labels

- **WHEN** the detail sheet of an account with usage opens
- **THEN** it shows the requests, tokens and cost of "Last 7 days" and of "Lifetime", and no unlabelled usage total

### Requirement: Account row actions update the row in place

Priority, Pause or Resume and Re-login SHALL be available inline in the accounts table. After a successful priority, pause, resume, alias, warm-up or trusted-access mutation the row SHALL show the new value without reloading the page, and the list SHALL then be refetched; Re-login SHALL open the OAuth flow targeted at the account and refetch the list when it completes.

#### Scenario: Inline priority

- **WHEN** the operator selects Preserve in a row's priority select
- **THEN** `PUT /api/accounts/{id}/routing-policy` is sent and the row shows Preserve without a page reload

#### Scenario: Inline pause

- **WHEN** the operator presses Pause in an active account's row
- **THEN** `POST /api/accounts/{id}/pause` is sent and the row shows the paused status with Resume
