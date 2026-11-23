## 1. HTTP bridge settlement ownership

- [x] 1.1 Run ambiguous post-send failure settlement in one cancellation-deferring task and verify it preserves durable-unknown-before-cleanup ordering with no resend.

## 2. Regression coverage

- [x] 2.1 Add a request-submit regression that cancels during durable `unknown` marking and verifies cancellation propagates only after the operation write, queue removal, gate release, and pending finalization.
- [x] 2.2 Cover durable `unknown` write failure and verify local cleanup still completes without replay.
- [x] 2.3 Cover cancellation during pre-dispatch cleanup and verify rollback completes before cancellation propagates without an upstream send.

## 3. Validation

- [x] 3.1 Run the focused HTTP bridge tests and strict OpenSpec validation.
