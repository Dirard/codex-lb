# API Keys Context

See `openspec/specs/api-keys/spec.md` for normative requirements.

## Request-aware reservation estimate

The admission budget for token and `cost_usd` limits (Requirement
"Request-aware API-key usage reservations") sizes the input side from the
forwarded request payload: `min(utf8_length(serialized_payload_minus_caps), 8192)`.
Two implementation notes keep that value exact while avoiding redundant work on
the hot path:

- **Shared dump.** `ResponsesRequest.to_payload()` is deterministic, so the
  HTTP bridge prepare path computes it once and threads the same dict through
  client-metadata derivation, the forwarded `response.create` frame and
  `estimate_api_key_request_usage(payload, upstream_payload=...)`. The budget,
  frame bytes and input fingerprints are byte-identical to computing each stage
  from its own dump. When the prepare path rewrites the request (replayed
  side-effect tool-call dedupe under `previous_response_id`) the caller's dump
  is discarded and recomputed from the rewritten request, so the forwarded
  frame never carries un-deduped input. Callers that do not hold a dump keep
  the default single-argument form.
- **Early-exit serialization.** Because the estimate is capped at 8192 bytes,
  the estimator returns the cap as soon as it is proven: an `instructions`
  string of 8192+ characters alone suffices (a JSON string literal is never
  shorter than its character count), otherwise the payload is streamed through
  the same `sort_keys`/`ensure_ascii=False` encoder and stopped once 8192 bytes
  have been produced. Sub-cap payloads still yield the exact serialized length.
  The opaque-context checks (`previous_response_id`, `conversation`, file or
  image references) run before either shortcut, so the conservative `None`
  budget is unchanged.

Edge: a lone surrogate (`"\ud800"`) anywhere in the payload used to raise
`UnicodeEncodeError` (HTTP 500) from the full dump. It now raises only when it
sits in a chunk that is actually UTF-8 encoded, i.e. within the first ~8 KiB of
serialized output and not inside a string literal that the length shortcuts
(`instructions` >= 8192 chars, or a single chunk that alone covers the
remaining budget) prove the cap without encoding. Surrogates skipped that way
yield the 8192 cap like any other large payload.

## Astra Codex cost estimates

The built-in `gpt-6-astra` entry estimates subscription-backed Codex usage at USD 10/1/50 per million input/cached-input/output tokens. Fast (`priority` or `fast`) is 2.5x; the existing Flex tier uses half the base rates. Versioned `gpt-6-astra-*` IDs resolve to the same entry. Other model families retain their existing prices.

The [ChatGPT token-based rate card](https://help.openai.com/en/articles/20001415-chatgpt-rate-card-enterprise-token-based-pricing#gpt-6-astra-codex-long-context-exception), checked on 2026-09-07, explicitly excludes Astra usage in Codex from the >272K multiplier and excludes cache-write charges. These are cost estimates, not a new subscription-credit meter or an assertion about a particular customer's invoice. The [direct API tariff](https://developers.openai.com/api/docs/models/gpt-6-astra) differs. External model sources continue using their configured prices (or zero when unpriced); they never inherit the built-in Codex estimate.

For example, 300K input tokens including 50K cached, plus 100K output tokens, cost an estimated USD 7.55 in standard Codex mode or USD 18.875 in Fast mode. Crossing 272K does not change those rates.

The Astra data migration corrects retained subscription request costs and applies their differences to already-folded report totals, preserving contributions whose raw logs were pruned. It cannot reconstruct deleted usage. Old key-limit counters and reservation settlements are intentionally unchanged: retrospective reporting does not consume the client's current enforcement budget. The correction runs on upgrade without upstream requests and keeps corrected costs on downgrade.

## GPT-6 Sol and Luna estimates

Sources checked on 2026-09-23: [Sol API model](https://developers.openai.com/api/docs/models/gpt-6-sol), [Luna API model](https://developers.openai.com/api/docs/models/gpt-6-luna), [Codex pricing](https://learn.chatgpt.com/docs/pricing), [Codex Fast](https://learn.chatgpt.com/docs/agent-configuration/speed), and the [ChatGPT USD rate card](https://help.openai.com/en/articles/20001415-chatgpt-rate-card-enterprise-token-based-pricing). The built-in estimates use USD 2/0.2/10 for Sol and 0.1/0.01/0.5 for Luna per million input/cached/output tokens. They are reporting equivalents, not a measurement of remaining subscription credits.

Both model pages publish a >272K context tier; the explicit Codex exception in the USD rate card still names Astra only. Sol/Luna therefore retain that tier, with Codex Fast 2.5x applied to the applicable Standard rates. API Fast's 2x multiplier is not substituted for the Codex multiplier. Flex uses half the applicable Standard rates. No separate cache-write or Batch charge is inferred.

For 300K input including 50K cached, plus 100K output, Standard estimates are USD 2.52 for Sol and 0.126 for Luna. Fast estimates are USD 6.30 and 0.315. Exactly 272K remains in the short-context tier. External model-source overrides stay independent, and unrelated GPT-6 names do not fall back to these models.

The shared calculator supplies new request persistence and key-limit settlement; no new pricing infrastructure or configuration is required. This addition does not include historical backfill, schema migration, image publication, or deployment. Existing historical sums remain as recorded.
