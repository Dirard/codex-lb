## 1. Compatible wire requests

- [x] 1.1 Reproduce the parameter rejection in the authenticated probe test and verify subscription HTTP/WS/compact removal and external preservation.
- [x] 1.2 Use at least the normal uncapped subscription output estimate and verify the existing remaining-budget clamp and actual-usage settlement.

## 2. Verification and rollout

- [x] 2.1 Run targeted race tests, full Go tests, vet and build; synchronize and validate affected specs/context.
- [x] 2.2 Back up and update local port 2456, verify health/data, perform exactly one authorized live Luna probe and record its outcome: failed with invalid_stream, no usage known.
- [x] 2.3 Compare original stream handling with safe captured evidence, reproduce and fix invalid_stream, and verify real Luna probes (additional probes now authorized). Remove temporary diagnostic code before archiving.
