## Why

On a copy of production (go-v1.5.0, about 10 000 requests an hour) `GET /api/dashboard/projections` takes 221 ms, which makes the Groups page take 297 ms while its main endpoint answers in 123 ms. Almost all of it is the weekly pace's key attribution, which groups every raw usage event of the last two hours (20 000 events there, 75–98 ms even in C SQLite). No page shows that attribution: the old dashboard card that listed "Top consumers · last 2h" was removed with the overview redesign, and the Groups page's pool runway card shows headroom, runway, next relief and the throttle-to and add-Pro recommendations only. The rollups cannot serve an exact sliding two-hour window cheaper: about half of such a window lies in partial hours read raw.

## What Changes

- `GET /api/dashboard/projections` returns the weekly credit pace with an empty `topApiKeys` list and no longer reads usage events for it; every other field is unchanged.
- The deprecated `GET /api/dashboard/overview` keeps computing the key attribution of the last two hours for dashboard tabs opened before the redesign, until it is removed.

## Capabilities

### Modified Capabilities

- `go-runtime`: the read-only weekly-credit pace's key attribution belongs to the deprecated overview only.

## Impact

- Code: `internal/application` (weekly pace assembly).
- API: `topApiKeys` of `GET /api/dashboard/projections` is always empty; the field stays for compatibility.
- Schema and operations: none.
