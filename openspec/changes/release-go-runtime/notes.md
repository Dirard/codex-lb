# codex-lb Go 1.0.0

First independent Go release of this fork: a single statically linked server
with SQLite and an embedded administration dashboard. The old Python/Rust
implementation is retired from the source checkout; its Git history remains.

## Included

- ChatGPT/Codex accounts, external Responses/Chat-compatible sources, account
  groups, per-key limits, reports, editable model prices and offline legacy import.
- Key-login reports with personal limit usage, read-only same-group key
  mini-statistics, capacity-weighted group subscription usage and remaining
  purchased-credit totals. Unknown, zero and unlimited balances are distinct.
- Purchased credits can cover exhausted included subscription windows. Actual
  newer upstream quota refusals are not bypassed by stale credit evidence.
- Both `/backend-api/codex` and `/v1` client base URLs remain supported, including
  native Responses WebSocket routing.

## Downloads and operation

Choose `linux-amd64` for an x86-64 VPS or `linux-arm64` for an ARM64 VPS. Verify
the archive against `SHA256SUMS`, extract it, and run `./codex-lb version`.
Each archive includes `codex-lb`, the license texts and a systemd template.
The binary uses the host CA certificate store; no Python, Node or Rust runtime
is needed. Serve a remote deployment behind HTTPS.

To run a new installation:

```sh
./codex-lb serve --data-dir /srv/codex-lb-go --listen 127.0.0.1:2455
```

For legacy migration, stop writes and make a consistent backup of the legacy
SQLite database plus its matching encryption key. Import into a NEW directory:

```sh
./codex-lb import-legacy --source /backup/legacy-copy.sqlite --source-key /backup/encryption.key --data-dir /srv/codex-lb-go --dry-run
./codex-lb import-legacy --source /backup/legacy-copy.sqlite --source-key /backup/encryption.key --data-dir /srv/codex-lb-go
```

Do not point the Go server directly at a legacy database. Preserve the old
binary, database and encryption key for rollback; switching back after new
traffic requires accounting for new writes, not simply restoring an old snapshot.
Existing Go installations must back up both their database and encryption key
before replacing the binary. Publishing this release does not update any server.

The runtime contract and detailed migration/operation notes are in
[`openspec/specs/go-runtime/context.md`](https://github.com/Dirard/codex-lb/blob/go-v1.0.0/openspec/specs/go-runtime/context.md).

## Verification

Full Go tests, race detection and vet; 1236 frontend tests; additional real-binary
frontend contract tests and offline browser checks. Release artifacts are built
for Linux amd64 and arm64; runtime smoke checks execute on amd64. Production
capacity depends on request size and upstream behavior; this is not a guarantee
of VPS throughput. Historical legacy-only specs retain their pre-existing strict
validation warnings; the current Go contract and release change validate.
