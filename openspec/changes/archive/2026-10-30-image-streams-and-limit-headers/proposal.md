## Why

CanonKeeper draws saga illustrations through codex-lb and asked for three changes, which the owner confirmed on 2026-10-30.

- **Streaming on `/backend-api/codex/images/*`.** codex-lb currently rejects `stream: true` with 400 before dispatch. A probe on 2026-10-30 (Pro account, direct to the subscription) showed the actual behavior:
  - a generation with `stream: true` answers `text/event-stream` with `keepalive` events about every 15 s and one `image_generation.completed` event carrying the image, `usage`, `quality`, `size` and `generation_id`;
  - `partial_images` is ignored, so no draft frames arrive;
  - an edit with `stream: true` answers plain JSON.
- **Reset headers on the final image-limit refusal.** codex-lb returns the upstream 429 body but drops `Retry-After`, `X-Codex-Active-Limit` and the image window headers. Clients cannot show when the limit resets.
- **Image-limit refusals on `/responses` with the hosted `image_generation` tool.** codex-lb treats any 429 `usage_limit_reached` as account quota exhaustion. If the subscription refuses because the separate image window is empty, the account would be marked `quota_exceeded` and leave text routing. Production showed no such marks in the last 48 h, so this is prevention.

## What Changes

- Image routes accept `stream: true`; a non-boolean `stream` is still 400. The request is sent with `Accept: text/event-stream`.
  - An upstream event stream is relayed event by event and settled from the `usage` of the completed event.
  - An upstream JSON answer is returned as JSON.
  - A stream that ends without a completed image ends with an `error` event and keeps its reservation for reconciliation.
- On a final upstream 429, the image routes return the upstream `Retry-After`, `X-Codex-Active-Limit` and `X-Image-Gen-*` headers. When the active limit is an image limit, they also return the `X-Codex-Primary-*` and `X-Codex-Secondary-*` window headers.
- On Responses over HTTP, and on a WebSocket handshake, a 429 whose `X-Codex-Active-Limit` names an image limit becomes `image_generation_limit`:
  - no account quota mark, no replay on another account, the attempt settled uncharged;
  - the client receives 429 with `usage_limit_reached` and the same reset headers.

## Capabilities

### Modified Capabilities

- `go-runtime`: image routes stream and pass reset headers; image-limit refusals on Responses leave the account usable.

## Impact

- Code:
  - `internal/adapters/upstream`: classify image-limit 429 and keep its reset headers;
  - `internal/adapters/provider`: image streaming;
  - `internal/application`: image-limit settlement on Responses, stream pass-through;
  - `internal/adapters/httpapi`: SSE relay and header pass-through;
  - `internal/domain`: image-limit header rule.
- API: `stream: true` accepted on the image routes; reset headers on image-limit refusals; a new error code `image_generation_limit` in usage events.
- Schema: none.
