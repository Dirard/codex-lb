# Subscription period display

The subscription end in [the account identity specification](./spec.md) comes from `chatgpt_subscription_active_until` in the saved ID-token. Both Accounts and dashboard summaries expose this metadata without a separate database field or upstream request.

This is the period recorded when authorization metadata was issued, not a live billing lookup. Renewal can leave an old end date until the ID-token is refreshed. For example, an account renewed after its recorded September 5 end may still show an elapsed recorded period while upstream access continues. The display never changes account eligibility or routing.
