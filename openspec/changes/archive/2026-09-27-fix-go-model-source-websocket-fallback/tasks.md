## 1. Transport repair

- [x] 1.1 Confirm the live failing frame and original transport contract; validate the scoped design and delta specification.
- [x] 1.2 Add the pre-reservation fallback guard and public route regressions covering prewarm, generation, HTTP retry, authorization and explicit native transport; run the focused Go tests.

## 2. Verification

- [x] 2.1 Run race/full Go tests, vet and an isolated rebuilt-runtime Codex CLI E2E; verify completion and no uncertain test reservations without updating the existing service.
- [x] 2.2 Synchronize the main runtime spec/context and archive only when this repair is verified; retain the broader full-product verification goal as unfinished.
