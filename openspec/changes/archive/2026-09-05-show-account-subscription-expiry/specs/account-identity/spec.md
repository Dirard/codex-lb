## ADDED Requirements

### Requirement: Account summaries expose the recorded subscription end

The account-list and dashboard-overview APIs SHALL expose nullable `subscriptionActiveUntil` as an ISO 8601 timestamp with timezone, obtained only from the saved ID-token's `https://api.openai.com/auth.chatgpt_subscription_active_until` claim. This field SHALL remain available when authentication-status details are omitted. Missing or malformed subscription metadata SHALL yield null without discarding other valid identity claims. Producing this metadata MUST NOT issue upstream requests or change account eligibility, quota state, or routing.

#### Scenario: Subscription metadata is present

- **WHEN** a saved ID-token includes a valid subscription end timestamp
- **THEN** both APIs expose that timestamp in the corresponding account summary
- **AND** the value is independent of token expiration and quota reset timestamps

#### Scenario: Subscription metadata is unavailable

- **WHEN** the saved ID-token or its subscription timestamp is missing, unreadable, or malformed
- **THEN** the summary exposes a null subscription end
- **AND** otherwise valid account identity and existing authentication status retain their previous behavior

#### Scenario: Saved subscription period has elapsed

- **WHEN** the recorded subscription timestamp is in the past
- **THEN** the API still returns the recorded timestamp
- **AND** this metadata does not disable the account or authorize account failover
