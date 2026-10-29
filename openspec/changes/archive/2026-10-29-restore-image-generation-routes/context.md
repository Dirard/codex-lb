# Context: restore-image-generation-routes

## How Codex generates images (openai/codex rust-v0.165.0, 360319d8)

- The built-in `$imagegen` tool (`codex-rs/ext/image-generation`) posts JSON to `{base_url}/images/generations` and `{base_url}/images/edits` (`codex-api/src/endpoint/images.rs`). It never sends multipart, SSE or a trailing slash.
- Generation body: `{"prompt","background":"opaque"|"transparent","model":"gpt-image-2","quality":"auto","size":"auto"}`. The model is a constant in the client.
- Edit body: the same fields plus `images: [{"image_url":"data:<mime>;base64,…"} | {"file_id":"file-…"}]`, at most 5 entries.
- The client reads `created` and `data[0].b64_json`. If the response carries `background` or `quality`, their values must be within `transparent|opaque|auto` and `low|medium|high|auto`.
- Errors are read from the `error` object. A 429 `usage_limit_reached` is shown as an image usage limit.
- The client retries 5xx and transport failures up to four times. It does not retry 429.
- The Responses hosted `image_generation` tool is no longer used by the client; only old history items are read.

### When Codex shows the tool

`features.image_generation` must be on (the default), the local sign-in must not be on the Free plan, and the model must accept image input. In addition, one of these provider settings is required:

1. `requires_openai_auth = true` with a ChatGPT sign-in in Codex. The provider's `env_key` still supplies the codex-lb API key as `Authorization`.
2. `requires_openai_auth = false` with a non-empty `x-openai-actor-authorization` entry in the provider's `http_headers`. The value is a capability marker only. codex-lb does not treat it as authentication and does not forward it upstream.

Example of the second form:

```toml
[model_providers.codex-lb]
base_url = "https://<host>/backend-api/codex"
wire_api = "responses"
env_key = "CODEX_LB_API_KEY"
requires_openai_auth = false
http_headers = { "x-openai-actor-authorization" = "codex-lb" }
```

Start a new Codex session after changing the provider; capability state is fixed per thread.

## What the subscription endpoint honors (probed 2026-10-29)

Twelve direct requests were sent to `https://chatgpt.com/backend-api/codex/images/generations` with a Pro account, and two through `/backend-api/codex/responses` with the hosted `image_generation` tool. The requests varied these fields:

| field | values tried |
|---|---|
| `model` | `gpt-image-2`, `gpt-image-2.5-sunburst`, `gpt-image-2.5-flare`, `gpt-image-2.5-nonexistent` |
| `quality` | `low`, `high`, `xhigh`, `max`, `ultra` |
| `size` | `1024x1024`, `1536x1024` |
| reasoning | `reasoning: {"effort":"high"}`, `reasoning_effort: "high"` |

- Every images-endpoint request returned 200 with 1254×1254, `quality: "low"`, `background: "opaque"` and 21 input / 515 output tokens. The response also carried `generation_id` and the `x-codex-imagegen-request-id` header.
- Through Responses the backend echoed `{"model":"gpt-image-2-codex","quality":"auto","size":"auto"}` regardless of the sent tool config.
- Response headers describe the image limit, not the account's main quota: `x-codex-active-limit: imagegen_premium`, and `x-codex-primary-*` with a 1440-minute window on Pro. That is why image outcomes are account-neutral and an image-limit refusal only cools the account down for images.

OpenAI's announcement (2026-09-08) says ChatGPT Images 2.5 is available in ChatGPT and Codex on all plans. GPT‑Image‑2.5 Flare and Sunburst are API models (`gpt-image-2.5-flare`, `gpt-image-2.5-sunburst`, snapshots `-2026-09-08`). Through the API they honor `quality` up to `max`, sizes up to 3840 px and transparent backgrounds, at $5 / $8 / $30 per 1M text-input / image-input / image-output tokens. Routing those models to an OpenAI API key source is a separate follow-up.

## Accounting

- Reservations use the image model's builtin token rates.
- Settlement re-prices the event from its tokens like any subscription request. Image input tokens of an edit are counted at the text-input rate ($5 instead of $8 per 1M), because usage events keep one input counter.
- The subscription images endpoint returns `usage`, so a response without it is unknown usage and stays reserved for reconciliation.

## Failure modes

- No eligible ChatGPT account for the key: 503 `no_accounts`, from the shared selector.
- Every tried account is on its image limit: the last upstream 429 is returned to the client.
- An edit whose `file_id` was not created through codex-lb: 409 `file_owner_not_found`.
- A successful upstream body without image data: 502 `invalid_upstream_response`. Codex then retries it, as it would against the subscription.
