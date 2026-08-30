## 1. Regression Coverage

- [x] 1.1 Add an HTTP bridge reconnect regression that fails when a hard same-account reconnect loses continuity-owner quota bypass, while a soft reconnect remains ordinary affinity.
- [x] 1.2 Run the focused regression on the unfixed code and confirm the expected RED failure.

## 2. Implementation

- [x] 2.1 Preserve required continuity-owner provenance for owner-bound hard reconnect selection without widening bypass to soft reconnects.

## 3. Verification

- [x] 3.1 Run the focused HTTP bridge and required-owner selection tests plus Ruff on changed Python files.
- [x] 3.2 Run strict OpenSpec validation and verify the change artifacts are apply-complete.
