# Local concurrent-streaming measurements

These measurements use isolated temporary installations, synthetic loopback
providers and disk-backed SQLite WAL/FULL. They are not production/provider
SLAs. `GOMAXPROCS=1` limits Go execution, not the entire host to one vCPU.
Later runs with `--single-cpu` also pin the server to one allowed Linux CPU;
`server_cpu_id` distinguishes them from the earlier measurements below.
Client/provider CPU and memory are excluded from the server counters. The host
has other workloads, so compare repeated trials rather than a single tail.

## Baselines retained during development

`/tmp/codex-lb-ttft-baseline.2msVsJ/` contains the original `codex-lb`, the
`codex-lb-readpool`, `codex-lb-grouped`, and `codex-lb-cap256` stages. The last
includes grouped writes and the production transport-cap correction, before
the large-context memory changes. These temporary binaries are local comparison
artifacts, not a release or a production rollback installation.

## Durable write contention

Three alternating baseline/candidate trials: 128 metered Responses requests,
32 concurrent clients/keys, Chat Completions upstream, tiny input and five
20 ms chunks. Median-of-run TTFT p50 falls from 434.12 ms to 67.86 ms; TTFT p95
from 613.15 ms to 201.20 ms; p99 from 708.98 ms to 202.37 ms. Completion p50
falls from 626.20 ms to 240.16 ms. Both versions retain FULL durability and
correct per-key accounting. This comparison precedes context-memory changes.

Additional three-run comparisons with the same disk/durability rules:

- Native Chat, eight clients/keys and 128 requests: median-of-run TTFT p50
  34.89 → 13.04 ms, p95 76.75 → 45.56 ms; completion p50 166.90 → 135.11 ms.
- Translated Responses, 128 clients/keys and 256 requests: TTFT p50
  1945.33 → 386.02 ms, p95 2724.84 → 948.54 ms; completion p50
  2286.99 → 954.72 ms. The old global cap was 64, so the 128-client result
  includes both contention improvement and the admission-cap increase.

## Long-stream capacity and memory investigation

The production transport originally allowed only 128 simultaneous connections
to one host even after application admission was increased to 256. A runtime
regression now opens 256 actual HTTP streams through that transport; the
subscription application regression opens 256 SSE streams across 40 accounts,
finishes 246, cancels ten and preserves their unknown-billing reservations.
The WebSocket session pool had a separate 128-session ceiling. It is now 256;
the transport regression holds all 256 active, rejects the 257th without evicting
a peer, verifies terminal usage on every accepted stream and checks `Close`.

For actual-binary resource checks the stub can hold each stream after its first
event until all 256 are active (`--require-active 256`). Waiting application
requests therefore cannot masquerade as active provider streams. Direct
Responses and Responses-to-Chat translation are measured separately.

Before context-memory optimization, direct Responses, 256 metered keys, 250
20 ms chunks, `GOMAXPROCS=1`, no `GOMEMLIMIT`, all 256 streams active:

- Tiny input: peak RSS 92.48 MiB, CPU 1790 ms (25.20% across the workload).
- 256 KiB input each: peak RSS 652.73 MiB, CPU 6940 ms, TTFT p50 3188.14 ms.
- 1 MiB input each: peak RSS 2278.45 MiB, CPU 17750 ms, TTFT p50 12640.41 ms.

All completed with reported usage and zero errors, but the large-context memory
cost is not suitable for the intended 1 GiB VPS. These are investigation
baselines, not accepted final results. Raising only admission/transport capacity
does not fix redundant payload retention.

A longer pre-memory-change run pins the server to CPU 0 and checks kernel
`VmHWM`: 256 direct Responses streams with 32 KiB input each overlap for 20.29 s,
with peak RSS 182.18 MiB, total CPU 5530 ms and steady-state CPU 22.38%.
The provider emits 50 events/second per stream; all keys settle correctly and
descriptors return to 13. This is a stronger reference for the final run than
the short tiny-input check alone.

The separate cancellation run (128 requests, 32 clients, direct Responses)
had zero errors: upstream observed cancellation p50 40.09 ms, p95 85.66 ms;
server descriptors returned from 13 to 13 and no upstream streams remained.

## Validation scope

The scoped OpenSpec change and layered design validation pass. The repository-
wide strict spec check reports 51 passing and nine failing legacy specifications;
none of those nine files differs from Git HEAD. They are unrelated pre-existing
format errors, not changes to fix as part of streaming performance.
Final checks pass: 965 Go tests, the same 965 with the race detector, `go vet
./...`, and nine actual-binary dashboard contract tests. The final CGO-disabled
build is a stripped, statically linked Linux binary. A separate final 256-stream
run verifies every synthetic output character as well as usage and cleanup.

The intermediate code check passed all 947 Go tests; focused tests also verify
that a queued cancelled write never runs its callback and that 256 HTTP streams
can be opened to one host through the real runtime transport.

## Memory and CPU implementation decisions

Request fields now use immutable views into their original validated JSON body.
The caller buffer is never edited; field views have bounded slice capacity.
Duplicate or noncanonical top-level keys retain the old wire normalization,
including escaped spellings of `model`. HTTP route regressions caught and fixed
an initial wire fast-path discrepancy before acceptance. About 39,000 fuzz
examples also checked the view decoder against standard JSON semantics.

Capability scanning validates the whole document but does not copy unrelated
large values. The application retains normalized input items only when needed
for affinity, compaction, replay or continuation. Upstream Responses discards
parsed input/tool/system/instruction copies after capability validation while
keeping the complete original wire body; Chat translation reuses parsed items
and its stream state retains only needed scalar metadata. HTTP body ownership
remains with the standard transport, without unsafe mutation of `Body/GetBody`.

The isolated CPU profile found `responseInput` consuming 31.8% cumulatively:
an array was first attempted as a string, then decoded as an array, then every
already-validated item was decoded into another map. Shape selection and object
kind checks now reuse the completed JSON validation. Malformed input, non-object
items, unknown fields, Unicode, quota replay and scope checks remain covered.
No new storage engine, payload spool, unsafe JSON parser or dependency was added.

## Completed resource trials

Final code, direct Responses with Codex-shaped input message arrays, 256 metered
keys, one pinned CPU, `GOMAXPROCS=1`, normal Go GC settings, 50 events/s/stream:

- Tiny input: 256 active for 20.55 s, peak RSS 94.94 MiB, steady CPU 24.33%,
  TTFT p50/p95/p99 312.84/468.90/492.55 ms.
- 32 KiB each: 256 active for 20.08 s, peak RSS 147.13 MiB, steady CPU 21.16%,
  TTFT 1486.58/1653.09/1680.27 ms.
- 256 KiB each: 256 active for 20.08 s, peak RSS 367.75 MiB, steady CPU 20.82%,
  TTFT 3676.03/4335.21/4363.04 ms.
- 1 MiB each: 256 active for 5.02 s, peak RSS 1262.66 MiB, steady CPU 20.11%,
  TTFT 16713.72/17636.85/17756.95 ms, CPU 21820 ms across the workload.

Every trial reported zero errors, correct per-key usage and zero remaining
upstream streams. The 1 MiB synchronous burst processes 256 MiB of input on one
CPU; the initial parsing/admission burst must not be confused with steady stream
forwarding. A direct-stub calibration without a proxy had TTFT p50 325.76 ms and
p95 434.08 ms for 256 scalar-text requests of 1 MiB, so client overhead is not
the main explanation for the large-input delay.

The matching pre-memory-change 1 MiB message-array baseline, three runs on the
same pinned CPU, peaked at 2074.96–2136.45 MiB with median TTFT p50 16466.19 ms,
p95 17648.95 ms and CPU 21470 ms. Memory is materially reduced; large simultaneous
uploads do not show the latency gains measured for ordinary 8/32/128-client
requests. These stress results are not a claim of universal TTFT improvement.

Do not impose a 128 MiB hard process limit on this workload. In an intermediate
trial `GOMEMLIMIT=512MiB` caused GC pressure and failure to reach the required
overlap within the test's 30-second admission barrier. No such limit was added
to the runtime or service. For 256 concurrent 1 MiB contexts, provision more
than a 1 GiB VPS; 2 GiB is a reasonable starting point to validate on the actual
machine, not a guaranteed capacity promise. Tiny-input 256-stream operation
fits the preferred 128 MiB budget in this local workload.

Reproduce the 256 KiB sustained check with:

```sh
python3 scripts/compare-runtime.py --runs 1 --requests 256 --concurrency 256 \
  --keys 256 --metered --workload stream --protocol responses \
  --upstream-protocol responses --input-array --require-active 256 \
  --single-cpu --go-procs 1 --stream-chunks 1000 --prompt-bytes 262144 \
  --runtimes go
```
