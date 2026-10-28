## 1. Capacity planning

- [x] 1.1 `domain.PlanCapacity` memory tiers, derived bounds, descriptor clamp and overrides with table tests.
- [x] 1.2 `hostinfo.Detect` for cgroup v2/v1, physical memory and descriptor limit with filesystem fixtures; verified under a systemd scope with `MemoryMax=900M`.

## 2. Runtime

- [x] 2.1 Configurable SQLite readers, ingress budgets and upstream session bound; admission usage counters.
- [x] 2.2 Flags, GOMEMLIMIT handling, plan application before serving and the startup log.
- [x] 2.3 `GET /api/runtime/capacity` and the dashboard card.

## 3. Verification

- [ ] 3.1 `make check` and dashboard checks; sync the delta into `openspec/specs/go-runtime/spec.md` and archive after merge.
