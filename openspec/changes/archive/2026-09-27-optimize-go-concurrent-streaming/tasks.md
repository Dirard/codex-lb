## Implementation

- [x] 1. Preserve the baseline and localize TTFT overhead; prepare the scoped SDD change.
- [x] 2. Separate bounded committed readers from the durable SQLite writer and verify isolation/lifecycle/accounting regressions.
- [x] 3. Measure 8/32/128/256 concurrency and multiple keys on native Chat and Codex Responses; correct remaining measured bottlenecks without weakening invariants.
  UC3 durable group commit, UC4 context-copy/parse reduction and HTTP/WebSocket 256 capacity regressions pass. The preferred 128 MiB fits tiny requests, not all context sizes; measured sizing and large-burst latency limits are recorded in notes.md.
- [x] 4. Run integrated Go/race/vet and actual-binary contracts, synchronize specs and record honest performance results.
