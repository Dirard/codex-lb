# Subscription period display

The subscription end in [the account identity specification](./spec.md) comes from `chatgpt_subscription_active_until` in the saved ID-token. Both Accounts and dashboard summaries expose this metadata without a separate database field or upstream request.

This is the period recorded at the last interactive sign-in, not a live billing lookup: OpenAI keeps the subscription claims when it refreshes the ID token, so a renewal leaves the old end in place until the account signs in again. For example, an account renewed after its recorded September 5 end still shows an elapsed recorded period while upstream access continues. The Accounts page therefore prefers the end the Go runtime reads from ChatGPT in the background (see the `go-runtime` requirement «The subscription end is read from ChatGPT in the background») and uses this claim only until such a read exists. The display never changes account eligibility or routing.
