## Why

The owner cannot see how much of each Grok account's weekly pool is left. A probe found that the official CLI reads it from an undocumented billing route, which needs the scope `grok-cli:access`.

## What Changes

- Sign-in also requests `grok-cli:access`.
- The pool is read after sign-in and every 5 minutes, stored per account and shown on the Video page.
- Selection prefers accounts with more left and skips exhausted ones until their period ends.

## Capabilities

### Modified Capabilities

- `go-runtime`: the video pool reads and uses each account's weekly pool.

## Impact

- Schema: additive migration 45 adds four columns to `video_accounts`.
- Operations: accounts signed in before this release need one new sign-in to show a reading; they keep generating until then.
- Risk: the route is undocumented. A failed read keeps the last value and never blocks generation.
