## Context

Required-owner selection already constrains routing to one account, and the selector already supports ignoring standard quota status for an explicit set of account ids while preserving cooldown and the remaining eligibility gates. The missing link is that a proven hard-continuation owner is not added to that set. See `proposal.md` and the delta spec for scope.

## Goals / Non-Goals

**Goals:**
- Permit a bounded upstream probe of the same proven owner after normal cooldown expiry.
- Preserve owner-only routing and every non-quota eligibility check.

**Non-Goals:**
- Add a fixed grace-period timer or persist thread age.
- Let fresh or soft-affinity traffic use quota-exhausted accounts.
- Move delta continuations or account-scoped payloads between accounts.

## Decisions

After hard ownership has been resolved, include the required owner id in the selector's existing standard-quota ignore set for that selection only. This reuses the narrow selector flag: standard `RATE_LIMITED`/`QUOTA_EXCEEDED` status is ignored, while cooldown, backoff, authentication, health, activation, routing scopes, budget, traffic-class admission, and caps continue to run. Required-owner filtering prevents cross-account selection.

A persisted five-hour grace window was rejected because upstream continuation admission is not expressed as a stable local time contract. A broad quota bypass was rejected because it would send new work to exhausted accounts. The actual upstream result remains authoritative; an upstream 429 follows existing settlement and cooldown behavior.

## Risks / Trade-offs

- [The owner may still reject the continuation] → Existing upstream failure handling records the result and cooldown bounds the next probe.
- [The local quota reset remains in the future] → Only the proven owner-bound request ignores that single status; ordinary traffic remains filtered.

## Migration Plan

No data, configuration, or schema migration is required. Rollback removes the owner id from the per-selection quota-ignore set.
