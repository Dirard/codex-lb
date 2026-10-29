## Why

The Accounts page is the operator's only place to manage the ChatGPT accounts, yet it shows one account at a time: a list on the left and a detail pane that hides everything else behind a selection. Selecting an account calls ChatGPT synchronously (`GET /api/accounts/{id}/usage-reset-credits` refreshes usage from the provider), so browsing ten accounts sends ten provider requests. The detail shows "Request logs total" without saying it is a lifetime figure, and `GET /api/accounts` builds every account with about seven point queries plus a credential decryption, which grows with the installation (dashboard redesign use case 8, R42–R45; up to 100 accounts, Q10).

## What Changes

- `GET /api/accounts` is built from one batch read (`Store.ListAccountViews`): accounts, quotas, credit status, additional quotas, group memberships, lifetime usage totals, quota refusal times and token ciphertexts in a fixed number of statements, and the last 7 days of usage per account from the account usage rollups (day grain, with raw partial hours, the unfolded tail, the journal and folded legacy hours; raw events while the rollups are unusable). Every existing field keeps its name and value; only the token ciphertexts the response describes are decrypted. It adds `groups` (group ids), `usage7d` (`requests`, `totalTokens`, `costUsd`), `resetCreditsObservedAt` (when the in-memory reset-credit snapshot was taken) and the top-level `shell` status, and sends `Server-Timing`.
- The Accounts page becomes a table of every account (`AccountsTable`): search by alias, e-mail or id, status and group filters, sort by status severity (default), next reset (soonest or latest), 5 h remaining, weekly remaining, most reset credits or name (either direction), and Add account. Each row shows the label and e-mail, plan with the subscription countdowns, status with its reason, group chips, 5 h, weekly and (when present) monthly remaining with reset times, purchased credits, reset credits from the snapshot, warm-up, an inline priority select, inline Re-login or Pause/Resume, and a menu with Probe, Export auth, Redeem reset credit and Delete.
- Opening a row shows `AccountDetailSheet`: alias editing, quota windows with resets, the 7-day quota chart (refetched every 5 minutes only while open), usage labelled "Last 7 days" and "Lifetime", reset credits "available N as of HH:MM" with Check now, token information, the groups editor, and the routing, warm-up and trusted-access settings with the account actions. `/accounts?selected=<id>` opens it.
- The page polls only `GET /api/accounts` every 30 seconds and takes the shell status from it. Loading the page or opening an account never calls a provider; only Check now (`usage-reset-credits`), Probe and Re-login do. Inline mutations update the row in place and then refetch the list without reloading the page.

## Capabilities

### Modified Capabilities

- `go-runtime`: the batch-built account list with its additive fields, the Accounts page table, provider calls only on explicit actions, labelled usage periods and inline row actions.
- `frontend-architecture`: the account-list requirements become the table layout; the add, import, alias, export, delete and usage-reset requirements stay.

## Impact

- Code: `internal/domain` (account view contract), `internal/application` (reset-credit snapshot time, in-memory snapshot read without a storage read), `internal/adapters/sqlite` (`accounts_batch.go`), `internal/adapters/httpapi` (`listAccounts` from the batch read, `Server-Timing`), `web/` (`features/accounts`: table, toolbar, detail sheet, sorting, hooks; locales `en`, `ko`, `zh-CN`; MSW mocks).
- Schema: none.
- API: additive fields on `GET /api/accounts`; no endpoint is added, removed or deprecated.
- Operations: no new settings. Production has 9 accounts; reads are planned for up to 100.
