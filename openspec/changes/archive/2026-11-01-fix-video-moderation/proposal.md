## Why

On prod xAI reports moderation as HTTP 400 on a status read, with the body `{"code":"imagine:content-moderated","error":"Generated video rejected by content moderation.","usage":{"cost_in_usd_ticks":...}}`. Go-v1.10.3 ends such a job only after two minutes, as the generic `video_failed`, and records no cost, although xAI reports the same cost as for a finished video ($0.29 for 2 s, $0.43 for 3 s). So a client waits two minutes for a final answer, and the reported total is low by about $2 after six such jobs.

## What Changes

- An answer with the code `imagine:content-moderated` finishes the job at once, at any age, as `failed` with `video_moderated` and xAI's text.
- A finished job records the cost xAI reports in its final answer, whatever the outcome. The cost stays what xAI reports; it is not proven to be money charged to a subscription or a draw on its pool.
- Additive migration 48 corrects jobs that already ended this way from the answer kept on their rows.

## Capabilities

### Modified Capabilities

- `go-runtime`: moderation ends a job at once with its own code, and any finished job records xAI's reported cost.

## Impact

- Schema: additive migration 48 updates `video_jobs.error_code` and `cost_microdollars` of moderated jobs. It adds no column.
- API: `error` of such a job is `video_moderated` instead of `video_failed`, and `cost_usd` is present on a `failed` or `expired` job when xAI reported a cost. Totals on the Video page grow by those costs.
