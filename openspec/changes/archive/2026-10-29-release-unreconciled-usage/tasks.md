## 1. Release

- [x] 1.1 Store method releasing flagged reservations older than a cutoff through the release settlement, in bounded batches.
- [x] 1.2 Maintenance pass with a one-hour grace, 100-row batches and a five-second budget; log the count.
- [x] 1.3 Regression test: only old flagged reservations are released, live and recent ones stay, budgets are refunded, nothing is charged, pending log entries disappear.

## 2. Verification

- [x] 2.1 Gates and strict OpenSpec validation.
