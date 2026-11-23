## 1. Implementation

- [x] 1.1 Accept any origin on app session upgrades.
- [x] 1.2 Bypass cross-origin protection and answer CORS on `/v1/realtime/calls`.

## 2. Verification

- [x] 2.1 Tests through the full server handler: foreign origins reach sessions and calls, other routes stay refused.
- [x] 2.2 Gates, strict OpenSpec validation and release notes.
