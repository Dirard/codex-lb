# Запасные WebSocket не исчерпывают бюджет рабочих запросов
- Last edited with skill pack: `0.2.2`

## Title and scope

Увеличить бюджет клиентских соединений с учётом запасных пулов, не увеличивая лимит генераций и не размножая неограниченно крупные входящие сообщения. Не добавлять новые отказы ниже существующей рабочей ёмкости; измерить ресурсную стоимость.

## Planning anchor

`ProxyHandler.websocket` занимает глобальный слот до Upgrade и держит его до отключения. Канал из 256 слотов в `NewProxyHandler` не различает работающие соединения и пустые пулы. Upstream уже открывается лениво на response.create. Обновить существующий go-runtime contract; reuse WebSocket liveness, continuation и application admission. Reverse-documentation skipped: entrypoint, single caller and teardown are established directly.

## Connected groups or observed existing logic

- Ingress: native/v1 routes and trailing-slash aliases share websocket. Authentication precedes admission. Reuse the existing global connection channel with a larger capacity; do not add a lower per-key connection ceiling.
- Resource ownership: idle sockets retain readers and timers but not generation leases. Existing reader can retain a queued full JSON message per socket; simply raising the socket bound would also raise large-body backlog. Read only a 4 KiB prefix before acquiring a large-message permit and keep that permit through queued/active handling.
- Cancellation: response.cancel is handled by the reader outside the response.create queue. Small frames keep this path available while the large-message budget is full. Invalid input, failed upgrade, queue overflow, disconnect, drain and completed turns must release exactly their own leases.
- Application/upstream: global generation and provider-socket bounds remain 256 by default, with existing account/key limits and bounded queues. Do not evict active or continuation-owning upstream sockets, reset billing, or reinterpret capacity as provider quota.
- Validation: existing socket-capacity route test, focused counter/frame tests and the existing process-isolated compare-runtime harness. No frontend/database contract changes.

## Use cases

### 1. Принять подключение при свободном бюджете
запрос Upgrade --аутентифицировать ключ--> доверенная идентичность --занять свободный общий слот--> выделенный слот --выполнить Upgrade и обслужить соединение--> завершение --освободить слот--> доступный бюджет

Logic Details:
- Default: 4096 downstream sockets total. This accommodates an example of 256 working chats plus standby pools without adding a lower per-key ceiling. The exact Guardian pool size is not assumed to be proven. Existing key/account controls still govern actual generations.
- Reuse the existing channel semaphore and deferred release. No mutex/map, new setting or key-specific connection accounting is needed.
- Keep the existing 503/local_capacity_exceeded envelope and Retry-After for actual global connection exhaustion. Preserve the 120-second idle timeout and all request reauthentication.

Tests:
- description: More than 256 actual idle connections coexist with HTTP and a response.create on an existing socket; mere Upgrade never calls a provider.
- description: At a small injected test limit, one valid key can use every available connection; global overflow, invalid auth and failed upgrades do not leak slots.
- description: Disconnect/drain release their own global slots and the next permitted connection can enter.

### 2. Ограничить память входящих сообщений отдельно от пустых сокетов
доступный клиентский сокет --дождаться начала сообщения--> короткий префикс --проверить размер--> [короткое сообщение --обработать управление или запрос--> результат, крупное сообщение --занять ограниченный слот тела--> полное сообщение --передать владение очереди и worker--> результат --освободить слот тела--> свободный бюджет]

Logic Details:
- Waiting for a frame header/prefix does not reserve a large-body slot. Keep the existing 32 MiB per-message validation. The body budget is twice actual configured active-plus-queued response capacity (default 2 × (256 + 128) = 768), allowing both the current and staged next message. A narrow application getter avoids duplicating admission defaults; the permit follows the body through the one-element socket queue and active response.
- Frames of at most 4096 bytes do not need a large-body permit, so short cancellation/control frames remain readable. Small bodies remain bounded by socket count and the existing one-message queue.
- Do not add a lower aggregate byte ceiling: a 64 MiB cap would reject even 256 valid 256 KiB requests inside the existing generation capacity. Keep the existing 32 MiB message validation and report workload-dependent memory; the service budget is not a hard memory guarantee for arbitrary contexts.
- If a large-body slot is unavailable, discard the excess message using bounded scratch space under the existing 32 MiB read limit, report local capacity for that message and continue reading the same connection. Do not cancel its active response or authorize upstream replay. No admission mutex spans network I/O.
- Release on every validation rejection and on queue/active cleanup, including messages left when the reader exits. No DB transaction or admission mutex spans the response. HTTP body-reader admission stays independent.

Tests:
- description: A stalled or queued large frame holds one permit; completion, parse failure, queue rejection and disconnect return it once.
- description: At capacity, a second large frame is discarded without retaining its body, the active response on that same connection is not aborted, and a following short response.cancel still releases its owned body. Discard preserves the existing message-size limit.
- description: Existing canonical/alias, owner, failover, settlement, liveness and 256-stream tests remain valid.

### 3. Измерить цену запаса подключений
изолированный временный сервер --открыть авторизованные запасные сокеты--> измеренный idle бюджет --нагрузить существующий Responses путь--> измеренный сервер под нагрузкой --закрыть клиентов и проверить учёт--> проверенный результат

Logic Details:
- Extend scripts/compare-runtime.py instead of creating another benchmark framework. Use only fresh databases, synthetic keys and loopback stubs. Sample server PID RSS/CPU/fds separately from clients and preserve key-accounting checks.
- Compare baseline and populated pools on one CPU; test 4096 idle sockets across keys and coexistence with 256 active small-payload streams. Observe actual socket survival and cleanup, not just successful handshake count.
- The administrator clarified a 500–600 MB service budget on October 9; the former 128 MiB preference is not current. Fresh idle-socket measurements do not promise that thousands of retained large contexts fit this budget; report measured workload and limitations. Adjust proposed constants if resource results are unacceptable rather than removing bounds.

## Implementation checklist

1. [x] Map admission, reader ownership, request cancellation and existing load harness; fix the scope in OpenSpec.
2. [x] Increase global socket admission and bound large-message ownership with route regressions.
3. [x] Extend/run the existing offline load harness and record server resource measurements.
4. [x] Synchronize main spec/context; run full Go/race/vet and spec validation; archive verified work.
5. [x] Verify 512 active native generations with isolated experimental limits and report measured memory/CPU/latency at several context sizes without changing production defaults.

## Open questions

- Blocking: none. User explicitly requested implementation through SDD. No release or deployment authorization is inferred.

## Decision log

- 2026-10-09: choose bounded zero-config defaults, preserving active generation limits. No forced idle eviction or guessed upstream multiplexing; those would risk continuation state.
- Completeness check included the large queued-body amplification caused by more sockets. Consistency check preserves small cancellation access and all existing auth/quota boundaries. No standalone reverse-documentation or unrelated cleanup is required.
- 2026-10-09: user explicitly corrected the memory budget to 500–600 MB. No runtime/service memory limit is set by this change. Retain the existing comparison harness in one file (now about 700 lines with both WebSocket endpoints) to reuse its lifecycle, stub, accounting and process metrics rather than creating a second framework. The added protocol support is required for the user's 512-generation check; production code is not enlarged for this experiment.
- Final review raised the worst-case input bound. An initial 64 MiB byte guard was rejected after the user's requirement that resource measures not disrupt normal work: it created a new refusal below supported request concurrency. Count admission is sized for current plus staged messages (768 by default), matching the old worst-case backlog of 256 sockets with an active, queued and reading message instead of multiplying it by sixteen. Making all maximum-size contexts fit a hard RSS cap would require a separate request-storage redesign, not a lower hidden limit.
- 2026-10-09: preserve active work even at genuine input-count exhaustion by bounded discard of the excess message rather than closing the connection. Authentication, 32 MiB message validation and existing request admission remain unchanged.
- The proposed 3072 per-key socket ceiling was also removed: it would introduce a new refusal with unused global capacity. Reusing the original channel both preserves existing semantics and removes the unnecessary key-counter implementation.
- The user additionally requested verification of 512 active generations. Use a local Go build overlay to raise only the experimental application/HTTP/upstream-WS ceilings to 512, and extend the existing harness for native client WebSocket load. Production defaults remain 256. Measure server-only resources at multiple context sizes and verify all 512 actually overlap; record failure or budget overrun rather than treating queued work as active or claiming production readiness.
- Final offline trial: 4096 idle native sockets + 256 HTTP/SSE Responses on one metered key, 32 KiB inputs, 1000 chunks, server pinned to one CPU. Peak server RSS 419.16 MiB, 256 streams active together for 20.42 s, steady CPU 22.77%, TTFT p50/p95/p99 1156/1336/1343 ms; no errors, correct content/accounting, all idle sockets survived and FDs returned to baseline 13. This does not establish large-context or native-active-WS production capacity.
- Experimental 512-generation runs used native WS on both legs, 3584 standby clients, four metered keys and unique response IDs. At 32 KiB input: 512 active for 20.85 s, peak RSS 450.30 MiB, steady CPU 52.56%, no errors. At 256 KiB: 512 active for 20.95 s, RSS 748.86 MiB, CPU 51.16%, no errors. The latter already exceeds the administrator's memory budget.
- The corrected 1 MiB burst failed the 512-overlap goal: 508 reached the barrier, four returned connection_error, RSS peaked at 2898.31 MiB. The remaining 508 failed the fixture's overlap gate rather than independently failing upstream. Exact origin of the four transport failures remains undiagnosed; this experiment does not establish production readiness for 512. Earlier large-input failures from missing stub Pong or select() descriptor limits were discarded. The poll()-based control pump passed a 45-second native smoke and cancellation checks.
- Final implementation verification: go test ./..., go test -race ./... and go vet ./... passed; strict delta/main OpenSpec and layered-design validation passed. The experimental upstream 512-session ownership/cleanup regression passed three times normally and three times under the race detector. No commit, publication, deployment, production capacity increase or paid request was performed.
