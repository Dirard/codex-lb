## 1. Regression Coverage

- [x] 1.1 Add a direct-WebSocket regression proving a rejected proxy-injected anchor is compare-and-cleared and the next full request is not reinjected; verify it fails before implementation.
- [x] 1.2 Add controls proving a newer completion and a client-supplied invalid response are preserved.
- [x] 1.3 Add a process-path regression proving a retry-safe proxy anchor is retired before transparent replay is scheduled.

## 2. Implementation

- [x] 2.1 Retire the exact rejected proxy-injected anchor and its dependent continuity metadata from the live state without clearing newer state.

## 3. Verification and Release

- [x] 3.1 Run focused direct-WebSocket and proxy suites, Ruff, format checks, and strict OpenSpec validation (change strict PASS; aggregate remains at the pre-existing 50/58).
- [ ] 3.2 Commit and push `origin/fork`; build and deploy only after explicit approval.
