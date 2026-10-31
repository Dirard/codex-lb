## Why

A video job on prod stayed `pending` for over ten minutes while other jobs finished in about 48 seconds, and nothing showed what xAI was answering. xAI documents `respect_moderation` on a finished video, and the runtime ignored it: a `done` answer without a URL would keep the job `pending` for an hour.

## What Changes

- A moderated `done` answer finishes the job as `failed` with `video_moderated`; a `done` answer without a URL finishes it with `video_failed`.
- A due poll of a job older than two minutes that does not finish it logs a warning with the HTTP status and xAI's status word.
- A finished job without a video answers `error_message` with the reason in words, xAI's own text when it gave one; the dashboard job list shows it.
- The first-byte wait for upstream calls is three minutes, and a longer wait answers 504 `upstream_timeout`. Provider failure answers name their reason in `error.message`.
- Client polls no longer push the next scheduled poll forward while it is not due.

## Capabilities

### Modified Capabilities

- `go-runtime`: video jobs end on a moderated or empty result and say why, unfinished jobs leave a trace, and provider failures name their reason.

## Impact

- Schema: additive migration 46 adds `video_jobs.error_message`.
- API: new field `error_message` and new `error` value `video_moderated` on `GET /video/v1/generations/{id}`; new `errorMessage` in the dashboard job list; `upstream_timeout` 504 where a wait past the bound answered 502 `upstream_unavailable`.
- Logs: no tokens, upstream request ids, prompts or URLs.
