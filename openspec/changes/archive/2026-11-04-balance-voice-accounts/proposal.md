## Why

go-v1.13.0 serves every voice request — GPT-Live calls, Realtime calls and app Realtime sessions — from the first active ChatGPT account in id order. All voice therefore lands on one account. A limit refusal never moves to another account, and the voice limit the subscription reports in `session.usage.updated.usage_limit` is ignored. The owner asked to fix this on 2026-11-04.

## What Changes

- Voice requests without a pinned conversation owner rank the key's active ChatGPT accounts:
  - accounts without a voice warning come first;
  - then accounts without an exhausted Codex window;
  - then the fewest live voice calls and sessions on this worker;
  - then the lowest window use.
- A Live call, Realtime call or app Realtime session that the subscription refuses with HTTP 429 before it exists moves to the next account, at most three in all. The refused account is cooled down for voice for its `Retry-After` (default 15 minutes). When every account is cooling down, the request gets 429 `voice_limit_reached` before dispatch.
- A non-null `usage_limit.status` on a Live call warns its account for `reset_seconds` (default 15 minutes), which ranks it last without refusing it.

## Capabilities

### Modified Capabilities

- `go-runtime`: one added requirement for voice account balancing.

## Impact

- No schema change. Counts and cooldowns live in each worker's memory.
- Provider traffic: a refused voice request may reach up to three accounts.
