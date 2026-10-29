# Context: image-streams-and-limit-headers

## Subscription stream (probed 2026-10-30, Pro account, direct)

`POST https://chatgpt.com/backend-api/codex/images/generations` with `{"prompt", "model":"gpt-image-2", "background":"opaque", "quality":"auto", "size":"auto", "stream":true, "partial_images":2}` and `Accept: text/event-stream` answered:

```text
HTTP 200, Content-Type: text/event-stream; charset=utf-8, X-Codex-Active-Limit: imagegen_premium

event: keepalive
data: {"type":"keepalive"}

event: keepalive                      (about 15 s later)
data: {"type":"keepalive"}

event: image_generation.completed     (about 22 s after the request)
data: {"type":"image_generation.completed","sequence_number":0,"b64_json":"<1.8 MB>",
       "created_at":1793308156,"generation_id":"44aff684-…","background":"opaque",
       "quality":"low","size":"1254x1254","output_format":"png",
       "usage":{"input_tokens":21,"input_tokens_details":{"image_tokens":0,"text_tokens":21},
                "output_tokens":515,"output_tokens_details":{"image_tokens":515,"text_tokens":0},
                "total_tokens":536}}
```

The stream had no `image_generation.partial_image` events and no `[DONE]` line; `partial_images` is ignored. The same request to `/images/edits` with one input image answered `application/json` (the non-streaming Images shape). codex-lb relays both answers as they are.

## Image limits

Every observed image answer carries `X-Codex-Active-Limit: imagegen_premium`. The Codex client reads a 429 with `error.type` `usage_limit_reached` together with that header and the `X-Image-Gen-*` window headers as an image usage limit with a reset time (`codex-api/src/rate_limits.rs`). codex-lb passes `Retry-After`, `X-Codex-Active-Limit`, every `X-Image-Gen-*` header and, for an image limit, the `X-Codex-Primary-*` and `X-Codex-Secondary-*` window headers. The account's `X-Codex-Plan-Type` and `X-Codex-Credits-*` headers stay inside codex-lb.

On Responses, only a 429 whose `X-Codex-Active-Limit` names an image limit is classified as `image_generation_limit`. Before this change such a refusal marked the account `quota_exceeded`, replayed the request on every other account and ended with 503 `no_available_accounts`. Production showed no such refusals in the 48 h before the change. How the subscription reports an exhausted image window inside a started Responses stream (for example a failed `image_generation_call`) has not been observed.
