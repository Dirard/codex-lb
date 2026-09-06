## 1. Contract

- [x] 1.1 Document the native connection/read deadline invariant and the unchanged replay boundary.

## 2. Implementation

- [x] 2.1 Correct the native response-head watchdog without changing the helper protocol or total request budget.
- [x] 2.2 Add real local wire regressions for a slow connect followed by a prompt head and for a post-dispatch head stall.
- [x] 2.3 Stop the affected Rust WebSocket task when command dispatch reports a terminal full or closed channel.
- [x] 2.4 Add deterministic coverage that overflow cleanup is request-scoped and preserves another active socket.

## 3. Verification

- [x] 3.1 Run the focused Python/native tests, Rust checks, and available OpenSpec validation.
- [x] 3.2 Complete the remaining assigned upstream audit and report any additional confirmed findings.
