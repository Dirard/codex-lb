## Why

The Codex fork's Go SDK reads an image-limit refusal first from the active limit's own window headers, named after the limit (`x-<limit>-*`, for example `x-imagegen-premium-*`). Pro accounts report the limit `imagegen_premium`. go-v1.7.0 passes `X-Image-Gen-*` and the `X-Codex-*` windows, but it drops headers named after any other image limit.

## What Changes

When `X-Codex-Active-Limit` names an image limit, the image routes and the Responses image-limit refusal also pass that limit's own window headers: `X-` plus the limit id with underscores turned into dashes, then `-*`. Plan and credit headers are still not passed.

## Capabilities

### Modified Capabilities

- `go-runtime`: reset headers of image-limit refusals include the active limit's own windows.

## Impact

- Code: `internal/domain` header selection.
- API: more reset headers on image-limit refusals.
- Schema: none.
