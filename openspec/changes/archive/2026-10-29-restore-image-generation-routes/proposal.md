## Why

Codex 0.165 generates and edits images with its built-in `$imagegen` tool by posting JSON to `{base_url}/images/generations` and `{base_url}/images/edits`. With ChatGPT sign-in those requests go to `https://chatgpt.com/backend-api/codex/images/…`, which the subscription serves. Behind codex-lb the provider `base_url` is `…/backend-api/codex`, and the Go runtime has no images routes. The rewrite deliberately dropped the legacy public Images API, so image generation fails for every Codex user of the pool. OpenAI-compatible clients of `/v1/images/*` have no route either. Users now need image generation urgently.

Direct probes on 2026-10-29 (one Pro account, 12 requests to the images endpoint, 2 through the Responses `image_generation` tool) showed that the subscription endpoint accepts any `model`, `quality`, `size` and reasoning field but ignores them: every request returned 1254×1254 at quality `low` with 515 output tokens. Through the Responses tool the backend replaced the tool config with `gpt-image-2-codex`. Choosing GPT Image 2.5 Sunburst or Flare, quality or size is therefore possible only through the OpenAI API, which is a separate follow-up. The subscription images endpoint already serves ChatGPT Images 2.5 to Codex on all plans.

## What Changes

- New proxy routes: `POST /backend-api/codex/images/generations`, `POST /backend-api/codex/images/edits`, `POST /v1/images/generations` and `POST /v1/images/edits`. Every body is JSON; `/v1/images/edits` also accepts OpenAI multipart, which is converted to JSON with data URLs.
- Each request is forwarded unchanged to the ChatGPT Codex images endpoint of one subscription account, and the upstream status and JSON body are returned unchanged.
- Local validation:
  - a non-empty `prompt` is required;
  - `model` defaults to `gpt-image-2` and must be a priced `gpt-image-*` model;
  - an edit needs 1–16 images;
  - `stream: true` is rejected.
- Key policy: allowed models apply to the image model, and an enforced model replaces it only when the enforced model is itself an image model.
- Account selection uses the shared balancer limited to ChatGPT accounts, without model-catalog filtering.
- Edits that reference a `file_id` stay on that file's owner account.
- Accounting:
  - the API-key reservation is taken before dispatch and settled once from the response usage, using the image model's token rates;
  - outcomes are account-neutral, because the image limit is separate from the account's quota.
- Image-limit refusals: an HTTP 429 refusal received before any output puts that account on a 10-minute image cooldown, and the request moves to another account, trying at most 4 accounts.
- Usage events record request kind `image_generation` or `image_edit` under the public image model. Diagnostics omit image payloads.
- Builtin prices are added for `gpt-image-2.5-flare` and `gpt-image-2.5-sunburst`.

## Capabilities

### Modified Capabilities

- `go-runtime`: image generation routes are part of the product again, backed by the subscription images endpoint.

## Impact

- Code:
  - `internal/application`: image operation, account-neutral settlement, selection flag;
  - `internal/adapters/provider`: images call;
  - `internal/adapters/httpapi`: routes, multipart conversion;
  - `internal/domain/pricing`: 2.5 prices.
- API: four new proxy routes; no dashboard API change.
- Schema: none; the request kinds are new values of an existing text column.
- Operations: Codex users need image generation eligibility on the client. That means either `requires_openai_auth = true` with a ChatGPT sign-in, or `requires_openai_auth = false` with a non-empty `x-openai-actor-authorization` provider header; see context.
