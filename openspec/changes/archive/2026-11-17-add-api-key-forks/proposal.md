## Why

The owner wants key holders to delegate: «также нужна возможность через апи создать/обновлять/удалять форк ключа, чтобы владелец ключа мог создавать дочерние ключи и управлять ими в рамках своих возможностей. также в ответе на запрос каждого форка ключа в заголовке показывать сколько запросов в минуту из общего ограничения занимает этот форк». Today only the administrator can create keys, and a project cannot split its key between its own services without sharing one secret.

## What Changes

- A key holder manages forks of their key with that key under `/v1/key/forks` (list, create, read, update, delete). A fork is a real API key, shown once on creation and linked to its parent.
- A fork never exceeds its parent: its models, efforts, tier, expiry and pool cap are validated against the parent when written; scope and inherited settings are the parent's; the parent's limits apply to fork traffic, and a disabled, expired or deleted parent disables its forks. Forks have no forks.
- The parent's request pool is shared by the parent and its forks; a fork may have its own cap within it. Fork answers carry `x-ratelimit-fork-used-requests` (the fork's requests in the pool over the last minute) and, with a cap, `x-ratelimit-fork-limit-requests`.
- Forks record who created and changed them; the administrator sees forks under their parent and keeps full control.

## Capabilities

### Modified Capabilities

- `go-runtime`: one added requirement for API key forks.

## Impact

- Schema 51 (additive): fork columns on `api_keys`. Fork secrets use a domain-separated hash, so older code cannot authenticate forks.
- No provider traffic changes.
