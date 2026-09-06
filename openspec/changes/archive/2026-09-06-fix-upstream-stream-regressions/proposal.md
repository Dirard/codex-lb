## Why

The upstream native-traffic changes included after stable fork `0615885d` turn known Responses transport failures into HTTP body truncation, including failures already known before response startup. Clients receive a body-decoding error instead of the proxy's actionable error. A local wire reproduction confirms the failure without contacting OpenAI.

## What Changes

- Preserve non-success HTTP error responses when startup fails before downstream response commitment.
- Finish started native Responses streams with the existing structured failure framing and cleanup instead of raising out of the ASGI body iterator.
- Retain downstream liveness frames while upstream is silent, without changing account selection or replay eligibility.
- Rebuild the packaged native helper when its Rust sources or Cargo inputs change under development Compose watch.
- Review the upstream range through `5ad638b6` and repair additional confirmed regressions with focused regression coverage.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `responses-api-compat`: Native transport failures preserve valid downstream HTTP/SSE framing and liveness without hiding failure or replaying ambiguous requests.
- `deployment-installation`: Development Compose watches every input used to build the native helper.

## Impact

The Responses API normalization and error boundary, route/wire regression tests, and the owning specification. No production rollout, database changes, new dependencies, or automatic upstream replay is authorized by this change.
