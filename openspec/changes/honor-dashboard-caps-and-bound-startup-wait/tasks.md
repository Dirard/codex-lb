## 1. Regression coverage

- [x] 1.1 Prove warm HTTP bridge stream-lease reacquisition honors a cached dashboard stream limit of `0` when the startup default is positive.
- [x] 1.2 Prove a startup capacity marker without a paired ready signal returns within the existing discovery deadline.

## 2. Implementation

- [x] 2.1 Pass the cached dashboard-derived effective caps into warm-session stream lease reacquisition.
- [x] 2.2 Apply the existing discovery deadline to the capacity-marker recovery wait and hand off the running stream on expiry.

## 3. Validation

- [x] 3.1 Run the focused regression tests and adjacent HTTP bridge/startup probe tests.
- [x] 3.2 Run Ruff formatting/lint checks for touched Python files.
- [x] 3.3 Validate the OpenSpec change and main specs with the available project tooling.
