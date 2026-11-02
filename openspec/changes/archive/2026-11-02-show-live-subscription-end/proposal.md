## Why

The Accounts page takes the subscription end from the saved ID token's `chatgpt_subscription_active_until`. On 2026-11-02 prod showed that OpenAI keeps that claim, and `chatgpt_subscription_last_checked`, from the last interactive sign-in when it refreshes the token. Four of six active accounts showed an elapsed period while they worked, among them skolcov, paid after its recorded end of 2026-10-22. The owner wants the current date, still followed by the +14-day reference.

An owner-approved read-only probe found the current end: `GET https://chatgpt.com/backend-api/subscriptions?account_id=<id>` answers with `active_until` (2026-11-05 for an account whose token still says 2026-10-05) and `will_renew`.

## What Changes

- The usage poller reads each polled ChatGPT account's subscription at most once per 15 minutes and keeps the last read in memory.
- `GET /api/accounts` returns that read as `subscription` (`activeUntil`, `willRenew`, `observedAt`) or null, still without calling a provider. `subscriptionActiveUntil` stays.
- The Accounts list counts down to the ChatGPT read when there is one, else to the token date, followed by `/` and that date plus 14 days. The tooltip names the source, the read time and renewal. "Subscription ended" appears only when a read taken after the end confirms it.

## Capabilities

### Modified Capabilities

- `go-runtime`: adds the background subscription read and the `subscription` field of the account list.
- `frontend-architecture`: the subscription countdowns follow the ChatGPT read.

## Impact

- Provider traffic: about four `GET /backend-api/subscriptions` per polled account per hour, beside about 120 usage and reset-credit reads. Paused, re-login-required and deactivated accounts are not read.
- No schema change: the read lives in process memory, so after a restart or an update the token date shows until the first read, within about a minute.
- API: one added nullable field in `GET /api/accounts`.
