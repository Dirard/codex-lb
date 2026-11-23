## 1. Astra pricing

- [x] 1.1 Add the canonical Astra entry and versioned aliases to the shared table; verify standard, cached, Fast/priority, Flex, and >272K boundary tests without changing other model behavior.
- [x] 1.2 Verify that a completed proxy request persists the Astra cost and uses it in API-key summaries and settlement with an integration test; no real upstream requests.
- [x] 1.3 Verify external Astra sources retain independent configured or unpriced costs, including above 272K; clarify the built-in subscription-routing scope against the API and Codex documentation and the supplied Codex exception.

## 2. Historical cost correction

- [x] 2.1 Add a bounded idempotent forward migration for retained subscription Astra costs and corresponding report deltas; verify aliases, cached/reasoning usage, retained folded rows, live-tail rows, pruned contributions and excluded custom sources on temporary databases.
- [x] 2.2 Verify the migration graph, upgrade/check/downgrade/re-upgrade behavior and preservation of old per-key enforcement counters and reservations with migration tests.

## 3. Validation

- [x] 3.1 Run relevant pricing, log, report, API-key and migration tests plus lint/type checks; validate OpenSpec strictly, verify the completed change and archive it with stable context.
