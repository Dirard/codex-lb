# Adaptive capacity: context

## Why tiers

The 1 GB defaults (512 generations, 128 queue, 4096 client sockets) were measured under mixed native WebSocket load on one CPU (peak RSS ~466 MiB) and approved by the administrator. A 1 GB VPS reports 900–990 MiB of `MemTotal`, so the 768 MiB–1.75 GiB tier keeps those numbers exactly. Each larger tier doubles the bounds; memory, not CPU, is what large contexts exhaust first. Byte-weighted body budgets for every proxy route are the next step and will make the generation bound less important as a memory guard.

## Example

A 6-CPU / 4 GB VPS without a cgroup limit logs:

```json
{"msg":"runtime capacity","cpus":6,"memory_bytes":4080218112,"memory_source":"physical","go_memory_limit_bytes":3060163584,"max_generations":2048,"max_queued":512,"body_readers":512,"client_websockets":16384,"upstream_websockets":2048,"sqlite_readers":16}
```

Under systemd, `MemoryMax=` (or `MemoryHigh=`) in the unit is honored automatically. To keep the previous fixed limits on a larger host, start with `--max-generations 512 --max-client-websockets 4096`.

## Failure modes

- Descriptor limit too low: client sockets shrink to fit; raise `LimitNOFILE` in the unit.
- Memory undetectable (non-Linux): sizing assumes 1 GiB and the source is `assumed`.
- GOMEMLIMIT exported: it stays in force; sizing uses it as 75% of the usable memory.
