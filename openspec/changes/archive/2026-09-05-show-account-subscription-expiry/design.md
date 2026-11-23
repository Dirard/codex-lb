## Context

The saved ID-token carries `https://api.openai.com/auth.chatgpt_subscription_active_until`. Read-only inspection confirmed this date for all five local accounts. One recorded date had already elapsed. Both account-list and dashboard APIs use the same account-summary mapper, but the dashboard omits authentication status.

## Goals / Non-Goals

Display the recorded subscription period and remaining time next to accounts. Changing routing, authentication refresh frequency, billing, or deployment is outside this change.

## Decisions

- Add a nullable top-level `subscriptionActiveUntil` summary field so it remains available when authentication status is omitted. Reuse the saved encrypted ID-token and existing parsing. Do not persist a duplicate date.
- Accept only a valid subscription timestamp; missing or malformed optional metadata yields an unknown date without discarding valid identity claims. Token `exp` and usage reset timestamps are not subscription dates.
- Reuse the existing countdown/date formatters and a shared presentation component across Accounts rows, dashboard cards, and dashboard list rows. Display the date and saved-metadata limitation in a tooltip. Keep the count current while the view remains open using existing time hooks if available.

## Risks / Trade-offs

The claim describes the period known at token issuance, so renewal can temporarily leave an elapsed date. Label that state as a recorded period that ended, not proof that upstream access stopped. No extra upstream calls or refreshes are made to obtain subscription metadata.
