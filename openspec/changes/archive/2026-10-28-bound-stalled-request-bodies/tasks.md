## 1. Admission

- [x] 1.1 Check declared lengths without reserving; charge all bodies as they arrive.
- [x] 1.2 Progress deadline (10 s idle, 2 min total) with 408 `request_body_timeout`.
- [x] 1.3 Stop charging at the route maximum and answer 413 for one more byte.

## 2. Verification

- [x] 2.1 Route tests: stalled heads hold nothing, stalled upload releases at the idle deadline, slow upload and long stream survive it, oversized gzip/chunked/audio bodies get 413 on the minimum plan.
- [x] 2.2 `make check`; sync the delta and archive.
