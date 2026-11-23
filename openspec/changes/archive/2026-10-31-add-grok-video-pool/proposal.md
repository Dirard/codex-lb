## Why

The owner wants to generate video with several of their own SuperGrok accounts through their codex-lb API keys. A probe on 2026-10-31 generated one video through `api.x.ai` with an OAuth token of the official Grok CLI. codex-lb has no way to pool those accounts and no job that outlives one request. Video must stay a separate function: it shares only the API key with the ChatGPT pool.

## What Changes

- A dashboard page **Video generation** manages its own pool of Grok accounts, imported from the official `grok` CLI's `auth.json`, and its own list of allowed keys. A key is denied until it is added.
- A separate REST surface authenticated by the existing API key:
  - `POST /video/v1/generations`;
  - `GET /video/v1/generations/{id}`;
  - `GET /video/v1/generations`.
- Generation accepts text, first frame, last frame, keyframes and references, and forwards them to `https://api.x.ai/v1/videos/generations`.
- Jobs are balanced across the pool and stay on the creating account. A refusal before execution moves the request to another account.
- A job's cost is recorded once, from the reported usage, by a poll or a background sweep. Video has its own accounting: it consumes no API-key limit and does not appear in usage reports.
- Grok accounts are not rows of `accounts`, so no other route or report can see them.

## Capabilities

### Modified Capabilities

- `go-runtime`: adds a separate Grok video generation pool and REST surface.

## Impact

- Schema: additive migration 44 adds `video_accounts`, `video_allowed_keys` and `video_jobs`. Older code ignores them, so rollback is safe.
- API: `/video/v1/*` proxy routes and `/api/video/*` admin routes.
- Dashboard: new page and navigation entry.
- Code: new packages and files only, plus route registration and composition; the Responses, ledger and account paths are unchanged.
