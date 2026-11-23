## Why

Video jobs on prod hung for an hour because xAI answered HTTP 400 to every status read (84 of 84 log lines), and the runtime treated that as a temporary failure and threw the body away. Four such jobs filled the account's four places, so every new request got 503 `video_no_accounts` (189 in 23 minutes) with a message that did not say why. The owner asked not to discard xAI's answers and statuses, because without them nothing can be debugged.

## What Changes

- An HTTP 400 answer to a status read of a job older than two minutes finishes it as `failed` with xAI's text.
- The job row keeps xAI's last answer: HTTP status, body up to 8 KiB and time, or the transport error of a read without an answer. The dashboard job list shows it with `url` values hidden.
- Every due poll logs the job id, age, HTTP status and status word: information under two minutes, a warning after. A finished poll logs the outcome and HTTP status.
- The limit of four unfinished jobs per account is removed. It was chosen without measurement: on prod xAI refused bursts of three by itself and accepted a new job while three hung, so only the local count blocked anything. Selection still prefers the least busy account.
- The 503 `video_no_accounts` message says that no account is active.

## Capabilities

### Modified Capabilities

- `go-runtime`: refused status reads finish the job, xAI's answers stay on the job row, and unfinished jobs no longer block new ones.

## Impact

- Schema: additive migration 47 adds `video_jobs.upstream_http_status`, `upstream_body` and `upstream_at`.
- API: new `upstreamHttpStatus`, `upstreamBody` and `upstreamAt` in the dashboard job list; a new message on 503 `video_no_accounts`. The REST surface is unchanged except that such a job now ends as `failed`.
- Logs: no tokens, prompts, URLs or response bodies, including on the new information lines.
