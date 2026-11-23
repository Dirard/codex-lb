## Context

See `proposal.md`. The submitter already publishes the session as closed before leaving the send boundary, but its post-send ambiguity write and cleanup currently run as ordinary awaits in the cancelled request task. The same gap exists after a pre-dispatch `ProxyResponseError`, where cancellation can interrupt operation rollback before the response-create gate is released. The codebase already uses one child task plus `_await_task_deferring_cancellation` when cleanup must outlive its caller.

## Goals / Non-Goals

**Goals:**

- Give the complete ambiguous-send settlement sequence one owner.
- Give pre-dispatch rollback and retirement cleanup one owner.
- Preserve durable `unknown` fencing, local cleanup ordering, and the original no-replay behavior.

**Non-Goals:**

- Change quota failover, operation replay eligibility, or public error classification.
- Add a new task registry, retry loop, or configuration.

## Decisions

Run the existing post-send failure sequence in one named `asyncio` task and await it through the shared cancellation-deferral helper. A single task is required because protecting only the durable write would still leave queue/gate cleanup cancellable between awaits. The settlement continuation records a failed `unknown` write as the existing persistence error, then continues through cleanup, pending-request finalization, and upstream close.

The pre-dispatch `ProxyResponseError` branch uses the same pattern around rollback, gate cleanup, and retirement. The outer request re-raises deferred caller cancellation only after the child finishes. Without cancellation it preserves the existing `ProxyResponseError`. The liveness-timeout owner path remains separate because it settles the whole session deque rather than this request alone.

## Risks / Trade-offs

- [Cleanup can wait on persistence during disconnect] → The write already precedes owner release; deferral only preserves that required ordering and uses the existing bounded database path.
- [A cleanup step can itself fail] → Preserve the current best-effort close behavior while keeping earlier queue/gate release and failure classification intact.
