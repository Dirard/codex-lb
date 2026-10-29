## 1. Implementation

- [x] 1.1 Builtin prices for `gpt-image-2.5-flare` and `gpt-image-2.5-sunburst`.
- [x] 1.2 Shared balancer: an image request selects ChatGPT accounts only and skips model-catalog filtering.
- [x] 1.3 Image operation: validation, key model policy, file-owner pinning, reservation and settlement, account-neutral outcomes, image-limit cooldown and failover, usage event kinds, diagnostics without image payloads.
- [x] 1.4 Provider: forward to the ChatGPT Codex images endpoint and require image data in a successful body.
- [x] 1.5 Routes: Codex-base and `/v1` generations and edits on the shared body budget; multipart edits converted to JSON.

## 2. Verification

- [x] 2.1 Route tests with a loopback ChatGPT stub: generation and edit round trip with settlement, multipart conversion, validation errors before dispatch, model policy, image-limit failover without account-health change, file-pinned edit without failover.
- [ ] 2.2 Gates and strict OpenSpec validation.
