# Seamless updates — context

Solution plan: [`specs/seamless-updates.md`](../../../specs/seamless-updates.md).

## How a seamless switch runs

1. The release is downloaded and verified exactly as before; `update-info` of the candidate reports `handoff` and `schemaFloor`.
2. The launcher decides: both the running worker and the candidate are handoff-capable, the launcher holds the listening socket, and the running schema is at least the candidate's floor → seamless. Otherwise the classic idle-gated switch runs unchanged.
3. Online backup (`VACUUM INTO` from a read-only connection while the worker serves).
4. The candidate starts with the inherited socket (fd 3), database lock (fd 4) and lifeline (fd 5), its instance id and the ids of live workers. It opens and migrates the database and becomes ready, with background work deferred. Peer load is relayed to it from now on.
5. Current/previous are persisted, the candidate is activated (it accepts on the shared socket), the previous worker is retired (stops accepting, joins background loops), and the candidate's background work starts.
6. The previous worker finishes its requests and exits (at most 30 minutes plus the shutdown grace); the active worker then marks reservations of dead workers as interrupted.

Example: a Codex CLI session has an idle WebSocket and an HTTP stream that will run for four more minutes. The administrator clicks Update. The WebSocket closes with 1001 and Codex reconnects on its next turn to the new version; the stream finishes on the old version, settles its reservation and the old process exits; new requests arriving during those four minutes go to the new version, which counts the old stream against the account's stream limit.

## Decisions and constraints

- One listening socket inherited by every worker keeps the kernel accept queue across the switch; `SO_REUSEPORT` would drop the queue of the closing socket.
- `updateProtocol` stays 1 because released launchers accept exactly 1; capability travels in optional fields.
- The launcher holds the inode `flock`; workers verify the inherited lock instead of taking their own, so a second installation is still refused.
- `BEGIN IMMEDIATE` costs nothing within one process (one writer connection) and turns cross-process snapshot conflicts into bounded waits.
- The schema floor is stored in the database because older code cannot know later migrations. Migrations 1–40 count as non-additive, so overlap across schema changes starts with schema-41 code.
- Workers survive only through the lifeline pipe, not `Pdeathsig`, so a later slice can replace the launcher image without restarting workers.

## Failure modes

- Candidate fails before activation: it is stopped; the running worker never stopped serving; no backup restore (the migration was additive for it).
- Activation fails: candidate stopped, previous selection restored, previous worker still serving.
- Previous worker does not acknowledge retirement in time: it is sent SIGTERM (ordinary drain with the shutdown grace) before the candidate's background starts.
- Delegated launcher crashes before its first worker activates: the next start of the bootstrap finds the unconfirmed delegation, does not delegate again and serves with its own launcher logic.
- Launcher killed: the lifeline closes and workers drain as on SIGTERM.

## Operations

Installations whose bootstrap binary (`ExecStart` in systemd, the image entrypoint in Docker) is go-v1.1.1 or older keep the classic switch, because the launcher process runs the bootstrap's update logic. Replace the bootstrap once with a release containing this change and restart; from then on switches are seamless and the launcher logic follows dashboard updates at each start.
