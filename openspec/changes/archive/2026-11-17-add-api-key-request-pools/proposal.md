## Why

The owner wants to give one project's API key a request pool: «я хочу в 1 проекте указать для его ключа 120rpm, превышение = ожидание высвобождения до 30сек (при превышении ожидания 429 с заголовком Retry-After), в ответе заголовок с кол-вом текущих доступных запросов в рамках пула». Today a key has only budget limits (tokens, cost, credits); nothing bounds how many requests it sends per minute.

## What Changes

- A key may have a request pool: 1-10000 requests per minute and a maximum wait of 0-60 seconds (default 30), set in the dashboard key editor or the admin key API (`requestPool`), applied to the next request without a restart.
- A model request over the pool waits in arrival order for a slot, which frees 60 seconds after it was taken. When its slot would free only after the maximum wait, it gets 429 at once with `Retry-After` and the OpenAI-style `rate_limit_exceeded` body. A client that disconnects while waiting consumes nothing.
- Every pooled answer carries `x-ratelimit-limit-requests`, `x-ratelimit-remaining-requests` and `x-ratelimit-reset-requests`. On the Responses WebSocket each `response.create` takes a slot, and a refusal is an error frame with the same values in `headers`.
- Model routes consume the pool; admin, dashboard, catalog, usage, file and control routes never do.

## Capabilities

### Modified Capabilities

- `go-runtime`: one added requirement for API key request pools.

## Impact

- Schema 50 (additive): two nullable `api_keys` columns. Older code ignores them.
- The limiter lives in the worker's memory; a seamless update starts the new worker with empty pools.
- No provider traffic changes: a refused request never reaches an account.
