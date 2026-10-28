## 1. Budget

- [ ] 1.1 Domain plan: `BodyBudgetBytes`, `BodyBudgetKeyBytes`, overrides; remove `BodyReaders`.
- [ ] 1.2 `BodyBudget` lease and charging reader with unit tests.

## 2. Routes

- [ ] 2.1 Responses HTTP, Chat Completions and embeddings charged; identity bodies read into an exact buffer.
- [ ] 2.2 Compact, control, files, transcription, realtime call creation and public warmup charged.
- [ ] 2.3 Responses WebSocket large messages charged by bytes; count permits removed.

## 3. Operator surface

- [ ] 3.1 Flags, startup log, capacity API, gauges, dashboard card.

## 4. Verification

- [ ] 4.1 Route-level tests: burst, mid-read refusal, partial failure, release exactly once, per-key share, shared WebSocket/HTTP budget.
- [ ] 4.2 Before/after peak RSS for a burst of large bodies with the release binary.
- [ ] 4.3 `make check`, dashboard checks, contract test; sync the delta and archive.
