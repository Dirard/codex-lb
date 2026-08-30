## Context

The load balancer already supports a request-scoped standard-quota bypass for a proven required continuity owner. Initial HTTP bridge creation supplies that ownership provenance, but `_reconnect_http_bridge_session` currently supplies it only for account-neutral recovery. A normal hard same-account reconnect therefore remains owner-restricted while still being filtered by local standard-quota status, which prevents the user's request from reaching ChatGPT and leaves the bridge creation in a capacity-wait loop.

## Goals / Non-Goals

**Goals:**

- Preserve required continuity-owner provenance during an owner-bound hard HTTP bridge reconnect.
- Let the existing thread's normal request reach the same upstream account despite local standard quota at zero.
- Keep the existing upstream error, settlement, and cooldown behavior authoritative.
- Keep new, unowned, and soft-affinity requests subject to ordinary quota filtering.

**Non-Goals:**

- Add a synthetic availability probe or another billable upstream request.
- Move account-owned continuation state to another account.
- Add a timer, setting, persistence field, or background retry mechanism.
- Broaden quota bypass to a soft reconnect that merely prefers its current account.

## Decisions

Reuse `preferred_account_is_continuity_owner` and the existing `required_continuity_owner` selection path. During reconnect, set the flag only when account selection is already owner-bound: account-neutral recovery, a hard same-account close/reconnect constraint, or an explicitly required preferred owner. Do not infer ownership merely because the preferred candidate happens to equal the current session account; soft reconnects choose that candidate by default and must not gain the bypass.

The selector remains unchanged. Its existing request-scoped owner-id allowlist bypasses only standard `RATE_LIMITED` or `QUOTA_EXCEEDED` status while preserving cooldown, auth, health, model, API-key, budget, traffic-class, and local-cap gates. The ordinary request is then sent through the existing bridge connection path. A real upstream rejection follows existing classification, settlement, client error, and cooldown handling; this change adds no retry.

Regression coverage exercises `_reconnect_http_bridge_session` with a hard same-account constraint and proves the reconnect selection is marked as a continuity owner. A paired soft reconnect assertion proves the bypass is not widened.

## Risks / Trade-offs

- [A soft reconnect is accidentally treated as an owner] → Gate on existing owner-bound reconnect inputs, not candidate equality, and retain a negative regression assertion.
- [A real upstream rejection is retried invisibly] → Reuse the current terminal error/cooldown path and add no new wait or retry branch.
- [The fix diverges from initial bridge creation] → Reuse the same ownership flag consumed by `_select_account_with_budget` instead of adding another bypass mechanism.
