# Runtime update operations

The normative contract is [spec.md](spec.md). This capability installs an existing
GitHub release; it does not publish releases or operate the VPS service manager.

## Installation and scope

One delivered binary contains a lightweight launcher and a server worker. The
launcher never forwards model traffic and opens the live SQLite database only
for the pre-switch backup. For handoff-capable workers (go-v1.2.0 and later)
the launcher binds the public port and holds the database inode lock and lends
both to its workers; older workers bind and lock by themselves. Linux amd64/arm64 versioned
release builds use managed launch by default; `serve --self-update=false` opts out.
A stable fixed listening port is required. Development builds still serve normally
but report self-update as unavailable.

Existing versions without this feature, including go-v1.0.4, need one ordinary
manual installation of a release containing it. They cannot be managed rollback
targets because they lack the `update-info` compatibility protocol. Thereafter
Settings contains check, update-to-version and rollback-to-version actions.
The same process works under systemd, another supervisor, or direct execution.
In Docker, the persistent data mount must be writable, private and executable;
the image and container engine are not changed. Existing outer supervisor
restrictions on creating child processes still apply.

## Storage and data safety

`<data-dir>/updates` contains private durable installation state, verified
executables identified by SHA-256, temporary staging and one pre-switch backup.
Current, previous and an in-progress candidate are retained; interrupted staging
is cleaned only while holding the installation lock. State writes and installed
files are synced before activation. The backup includes a consistent SQLite
snapshot and its encryption key. It is never restored automatically.

For example, install B while running A, create a key under B, then roll back to A:
that newly created key remains in the same database. The rollback button selects
only the actual retained previous executable, not arbitrary GitHub releases.
Rollback needs a version that can run on the current database: an identical
schema, or a handoff-capable version at or above the schema floor the newer
version reports. Otherwise the status explains that the pre-update backup is
needed.

The originally launched executable is not overwritten. On future starts the
launcher reads the durable selected server version from the data directory. The
digest of the last manually adopted bootstrap is recorded separately, so starting
that same binary cannot undo an administrator's later rollback choice. Only an
actually replaced bootstrap is treated as a fresh manual installation. A
launcher that finds a newer handoff-capable version selected re-executes it as
the launcher at start (same process id), so the update logic follows the
installed version from the next start; an unconfirmed delegation is not
repeated. Bootstraps up to go-v1.1.1 lack this and keep their own logic until
the bootstrap binary is replaced once.

## Switching and failure cases

Release discovery runs at startup and hourly; manual checks coalesce while running
and are limited to once per minute. Only complete stable Go releases from
Dirard/codex-lb are considered. Downloads stream to private staging, have size/time
bounds, and must match SHA256SUMS. Only the executable and the package's known
license/service auxiliary files are allowed; auxiliary files are not installed
or executed. Redirects stay on GitHub's HTTPS asset hosts, without credentials.

Downloading does not stop serving. A seamless switch (both workers
handoff-capable, running schema at least the candidate's floor) takes the
backup online, starts the candidate beside the running worker on the shared
socket, activates it, retires the previous worker and moves background work;
the previous worker finishes accepted requests (up to 30 minutes) and exits.
[The seamless-updates change context](../../changes/archive/2026-10-28-seamless-updates/context.md) describes
the sequence and failure modes. A classic switch waits up to ten minutes for
no active HTTP/SSE/Responses turn/realtime call, then briefly closes admission;
idle Responses sockets reconnect.
A fifteen-second prepared-gate lease reopens admission if control acknowledgements
are lost. Shutdown commits under that same gate, so a late stop cannot interrupt
work that began after lease expiry. A permanent operator shutdown is not reversible.

In a classic switch, only after the old process exits and a backup succeeds
does a candidate open data and take the same port. It remains fenced from traffic and background work until
readiness and durable current/previous metadata are verified. Startup failure
restores the prior executable without changing data. Failure to restore is explicit
and requires operator intervention; there is no hidden repair/retry loop.
An unactivated candidate that ignores graceful termination is killed and joined
before the previous worker starts; a serving worker is never forced through this
update-only cleanup. Integrity is checked before running even the descriptor.
Automatic or manual discovery does not erase a persisted failed-update result.

Unsupported initial storage can disable self-update while ordinary serving remains
available. An existing managed state is not ignored if inaccessible or corrupt.
On a noexec mount, direct serving is allowed only when the running bootstrap has
the exact digest of the selected current binary; a different selected version
is not silently replaced. Never delete managed state to force recovery.

Admin routes and private Unix control both enforce their authentication boundary.
The UI tolerates network errors and 5xx during switching, but real 401 responses
retain normal session-expiry handling. Key-report mode has no update controls.
