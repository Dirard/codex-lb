## 1. Ownership bugs

- [x] 1.1 Route-level regression test for a quota failover with `conversation`, then mark non-empty `conversation` account-bound.
- [x] 1.2 Regression test for compact quota replay with Lite history and a non-Lite enforced model, then keep the parsed Lite flag.

## 2. Durable writes

- [x] 2.1 Batch writer deadline after connection acquisition; batch size 64; runtime mutations through the batch writer.
- [x] 2.2 Reservation with affinity compare-and-set in one job.
- [x] 2.3 `FinalizeResponse` with nested savepoints; terminal after the commit.
- [x] 2.4 Chunked repricing with provenance pre-check, pending marker and resume; reserve/settle regression during a long reprice.

## 3. Fewer reads and JSON passes

- [x] 3.1 Price memo per selection and one selection snapshot read.
- [x] 3.2 Lazy history decryption with an awaited request-owned goroutine.
- [x] 3.3 Byte prefilters with fuzz equivalence; incremental `FilePinned`.

## 4. Storage

- [x] 4.1 Schema 34: alias context references, continuation stats triggers, reprice pending marker; migration test from 33.
- [x] 4.2 Analytics read pool; remove `TouchReservation` and `ReleaseStaleReservations`.

## 5. Verification

- [ ] 5.1 `go vet`, `golangci-lint`, `go test`, `go test -race`; before/after measurements in `specs/hot-path-writes.md`.
- [ ] 5.2 After review, sync the delta into `openspec/specs/go-runtime/spec.md` and archive this change.
