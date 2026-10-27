# 04 — Аудит транспортного слоя codex-lb (Go)

Зона: `internal/adapters/httpapi` (proxy/SSE/WS/native/operations/server), `internal/adapters/upstream/**`,
`internal/adapters/provider/**`, `internal/adapters/chatgpt/**`, `internal/adapters/streambuffer`, плюс
настройка `http.Transport`/`http.Server` в `cmd/codex-lb/runtime.go`.
База: `main` @ `0b674e97`. Во время аудита рабочее дерево параллельно менял кто-то другой (lint-правки в
`upstream/adapter.go`, `chat_stream.go`, `response.go`, `go.mod`, удаление `.agents/**` и т.д.). Это не мои
изменения. Номера строк ниже даны по текущему рабочему дереву и могут сдвинуться на 1–3 строки.

## Резюме

1. Транспорт не объясняет разницу в TTFT. Первый hop (upstream flush → клиент) стабильно занимает **0,10–0,25 ms**
   и для SSE, и для WS. TTFT и время до `response.completed` определяются **fsync SQLite**: один durable-коммит
   до dispatch и **четыре** между терминалом upstream и терминалом клиента. На NVMe это TTFT ≈ 8 ms и
   «terminal hop» ≈ 31–38 ms; на tmpfs — 1,0 и 0,9 ms (NET-4, вне зоны, но это корень метрики).
2. На больших контекстах время уходит на JSON: transport-код (httpapi+provider+upstream) сканирует и
   перекодирует тело запроса ~15 раз. На 1 MiB это 35–50 ms CPU на быстром ядре i9, E2E TTFT 70 ms (SSE) и
   94–109 ms (WS), 37/55 MB аллокаций на запрос (NET-5). Однопроходный прототип даёт ×4,8, `GOEXPERIMENT=jsonv2`
   без правок кода — ×2,9 по E2E TTFT (NET-6).
3. Подтверждены тестами три дефекта надёжности пула upstream WebSocket:
   - кадр, пришедший на простаивающий сокет, попадает в **следующий** ответ (NET-1);
   - медленный downstream-клиент блокирует reader, pong не читается, liveness-проба убивает здоровый ответ (NET-2);
   - «мёртвый» простаивающий сокет переиспользуется без проверки: 40 s зависания, ошибка после dispatch,
     неизвестный usage остаётся на сверку (NET-3).
4. Для upstream HTTP/2 нет health-check ping. Мёртвое h2-соединение подвешивает все мультиплексированные
   SSE-потоки (NET-9).
5. HTTP/1.1-соединение к провайдеру закрывается после каждого стрима, если конец тела приходит позже
   терминала: 20 запросов → 20 новых TCP+TLS (NET-10).
6. На событие: разбор upstream SSE/WS в `map` + лишняя копия стоят 3,4 µs и 36 аллокаций; лёгкий вариант —
   1,3 µs и 8 аллокаций (NET-7). `writeSSE` стоит 747 ns против 12 ns; rendezvous+ack — 530 ns против 73 ns (NET-8).
   Flush действительно делается на каждое событие, это хорошо для латентности.
7. Владение горутинами корректно: все pump/reader/probe ждутся (join), утечек не нашёл.
   Гипотеза «`ReadTimeout=2m` рвёт SSE» опровергнута: Go 1.26 сбрасывает deadline в `startBackgroundRead`.

## Окружение и методика

- i9-14900K (32 потока), Go 1.26.6, ext4 на NVMe (диск заполнен на 96%). Латентность fsync по `strace -T`
  под нагрузкой бенчмарка: p50 4,6 ms, mean 6,1 ms, p95 10,0 ms.
- Только loopback `httptest`: upstream-заглушка SSE/WS, реальные `provider.Adapter`, `application.Proxy`
  и `sqlite.Store`. Реального трафика к провайдерам не было.
- Временные `zz_audit_*_test.go` копировались в пакеты на время прогона и удалены. Исходники для
  воспроизведения и профили лежат в `.local/audit/scratch-transport/`:
  - `zz_audit_e2e_bench_test.go` — E2E (пакет `httpapi_test`);
  - `zz_audit_upstream_bench_test.go`, `zz_audit_upstream_probe_test.go` — пакет `upstream`;
  - `zz_audit_httpapi_internal_bench_test.go` — пакет `httpapi`;
  - `zz_audit_provider_bench_test.go` — пакет `provider`;
  - `cpu_*.prof`, `mem_*.prof`, `jsonv2_tests.log`.
- Переменные E2E-бенчмарка: `AUDIT_DB_DIR` (каталог SQLite, NVMe или `/dev/shm`), `AUDIT_DELTAS`
  (число дельт), `AUDIT_INPUT_BYTES` (размер ввода), `AUDIT_TRACE=1` (метки времени для сопоставления с strace).

## Измерения

### E2E: из чего состоит TTFT (1 дельта, 300 итераций, последовательно)

Метрики: `predispatch` — от запроса клиента до запроса на upstream; `firsthop` — от flush первой дельты
upstream до получения клиентом; `terminalhop` — от записи `response.completed` upstream до получения клиентом.

| Путь | SQLite | predispatch p50 / p95 | firsthop p50 | TTFT p50 / p95 | terminalhop p50 | total p50 |
|---|---|---|---|---|---|---|
| SSE→SSE | NVMe | 7,9–8,2 / 14,8–25,3 ms | 0,13–0,15 ms | 8,1–8,4 / 15–25 ms | 30,7–32,6 ms | 39–41 ms |
| WS→WS | NVMe | 7,5 / 9,6 ms | 0,10 ms | 7,65 / 9,7 ms | 37,6 ms | 45,4 ms |
| SSE→SSE | tmpfs | 0,89 / 1,52 ms | 0,10 ms | 1,03 / 1,70 ms | 0,90 ms | 1,87 ms |
| WS→WS | tmpfs | 1,23 / 1,91 ms | 0,085 ms | 1,34 / 2,02 ms | 1,20 ms | 2,45 ms |

fsync по фазам (strace `-ttt` + метки бенчмарка, 51 запрос): **1,04 fsync до dispatch** и **4,06 fsync между
терминалом upstream и терминалом клиента**, всего ≈ 5,1 на запрос.

### E2E: всплеск 2000 дельт (tmpfs, 40 запросов)

| Путь | ns/event (весь конвейер, вкл. заглушку и клиента) | allocs/event | B/event |
|---|---|---|---|
| SSE | 13 400 | ≈ 43 | ≈ 2,8 KB |
| WS | 18 300 | ≈ 58 | ≈ 4,5 KB |

Колбэк `upstream.(*HTTPAdapter).streamResponses.func1` даёт **70,6% всех аллокаций** процесса
(декодирование в `map`, `RawMessage.UnmarshalJSON`, `NewDecoder`, `bytes.NewReader`).
В CPU-профиле SSE-всплеска syscalls занимают ≈ 35%, планировщик (`park_m`, `findRunnable`) ≈ 23%,
`decodeExactJSON` ≈ 13% (с учётом заглушки и клиента в том же процессе).

### E2E: большие входы (tmpfs, 30 запросов)

| Вход | Путь | TTFT p50 | TTFT p95 | B/op | TTFT p50 с `GOEXPERIMENT=jsonv2` |
|---|---|---|---|---|---|
| 256 KiB | SSE | 19,7 ms | 25,3 ms | 9,7 MB | — |
| 256 KiB | WS | 26,1 ms | 34,5 ms | 14,1 MB | — |
| 1 MiB | SSE | 70–73 ms | 78–138 ms | 37 MB | **25 ms** |
| 1 MiB | WS | 94–109 ms | 110–159 ms | 55 MB | **35 ms** |

Профиль CPU для WS 1 MiB (31 запрос, 4,30 s): `provider.callChatGPT` — 1,38 s; из них `chatGPTWireBody` —
0,58 s и `upstream.OpenStream` — 0,80 s (`prepareRequest` 0,38, `websocketResponseCreate` 0,19,
`passthroughBody` 0,16). WS-reader в httpapi — 0,77 s, из них 0,50 s приходится на application.
Flat-профиль: `encoding/json.checkValid`, `stateInString`, `(*decodeState).skip` и `appendCompact` вместе дают
**≈ 75% CPU**.

### Микробенчмарки

| Бенчмарк | Текущий | Альтернатива |
|---|---|---|
| upstream `prepareRequest+passthroughBody+websocketResponseCreate`, 16 KiB | 0,67–0,90 ms, 197 KB, 368 allocs | однопроходный: 0,13 ms, 79 KB |
| то же, 256 KiB | 5,4–5,9 ms, 1,9 MB | 1,1 ms, 0,49 MB |
| то же, 1 MiB (реалистичное тело Codex), 43–47 MB/s | 18,8–20,6 ms, 6,9–7,3 MB | 3,9–4,5 ms, 1,8 MB; с jsonv2 — 1,9 ms |
| из них только `websocketResponseCreate`, 1 MiB | 9,3 ms, 2,7 MB | — |
| то же с jsonv2 (без правок кода), 1 MiB | 12,3 ms | — |
| `provider.chatGPTWireBody`, 1 MiB | 15,9–16,6 ms, 4,6 MB | — |
| `provider.requestModelAndStream`, 1 MiB | 2,9 ms | — |
| upstream SSE, колбэк на событие | 3364 ns, 36 allocs, 2240 B | лёгкий: 1265 ns, 8 allocs (jsonv2: 2451 → 910 ns) |
| `readSSE`, сам парсер | 123 ns, 2 allocs на событие | — |
| upstream WS, декодирование кадра | 2854 ns, 34 allocs | 1198 ns, 6 allocs (jsonv2: 692 ns, 1 alloc) |
| httpapi `writeSSE` | 747 ns, 3 allocs | 12 ns, 0 allocs |
| handoff событий (unbuffered + ack-канал) | 530 ns, 2 allocs | буферизованный канал без ack: 73 ns |
| аргументы tool-call в chat-стриме, 20 000 × 5 B | **153 ms, 1,06 GB** | `strings.Builder`: 0,118 ms, 0,5 MB |

### Поведенческие пробы (temp-тесты в пакете `upstream`)

- **Подтверждено.** Кадр после терминала на простаивающем сокете получает следующий ответ того же session:
  `second=resp_2 deltas=[{"delta":"STALE from resp_1"...}, {"delta":"turn resp_2"...}]`.
- **Подтверждено.** Медленный потребитель (1,5 s при ping 200/200 ms) приводит к
  `upstream connection_error: Upstream WebSocket did not answer a liveness probe`, хотя upstream здоров.
- **Подтверждено.** Повторное использование «чёрной дыры» среди простаивающих сокетов: ошибка через
  interval+timeout (в проде 30+10 s), `RejectedBeforeExecution=false`.
- **Подтверждено.** HTTP/1.1 SSE: когда конец тела приходит через 5 ms после терминала, 20 запросов дают
  20 новых TCP-соединений. Когда конец в том же сегменте — 1 соединение.
- **Опровергнуто.** `Server.ReadTimeout=2m` не отменяет длинный SSE: Go 1.26 `connReader.startBackgroundRead`
  вызывает `SetReadDeadline(time.Time{})`; тест с `ReadTimeout=1s` и стримом 3 s прошёл до конца.
- Все тесты пакетов `upstream`, `provider`, `streambuffer`, `httpapi` и `chatgpt` проходят с
  `GOEXPERIMENT=jsonv2` (без `-race`).

## Таблица находок

Приоритеты: P0 — срочно; P1 — высокий; P2 — средний; P3 — низкий. Трудоёмкость: S/M/L. «Гипотеза» помечена явно.

| ID | P | Категория | Доказательство | Почему важно | Исправление | Трудоёмкость | Риск | Как проверить |
|---|---|---|---|---|---|---|---|---|
| NET-1 | P1 | bug/reliability | `upstream/websocket_sessions.go:138` (`frames` cap 1), `:240-255` (reader читает и в простое), `:194-196` (сокет отдаётся без проверки буфера); `responses_websocket.go:81-206` (ID терминала сверяется только при interrupt, `:189`). Проба подтвердила | Чужие события попадают в следующий ответ. Устаревший терминал завершит новый запрос с чужим ID/usage, а настоящий ответ «съедет» в следующий. Нарушение «each client receives only its own events». Частота зависит от того, шлёт ли провайдер кадры после терминала (гипотеза) | (a) при выдаче переиспользуемого сокета неблокирующе проверить `entry.frames`; если там кадр — `discardLocked` и новый dial (до create, т.е. безопасно, `RejectedBeforeExecution`); (b) запоминать ID из `response.created` и отклонять терминал с другим ID (invalid_stream, сокет в retire); (c) reader помечает сокет dirty, если данные пришли без аренды | S | низкий | проба `TestAuditProbeStaleFrameReachesNextLease` должна давать 2 handshakes и без STALE; тест на несовпадение ID терминала |
| NET-2 | P1 | reliability | `websocket_sessions.go:245-251` (reader блокируется на `entry.frames <- frame` и не читает control-кадры); `websocket_probe.go:25` (`Ping` требует параллельного чтения); downstream-запись допускает 30 s (`proxy_websocket_stream.go:64`, `proxy_sse.go:82`), а ping timeout — 10 s (`adapter.go:49`). Проба подтвердила | Медленный клиент (10–30 s при допустимом лимите 30 s) убивает здоровый ответ уже после dispatch: без replay, usage на сверке, ответ потерян. Границы противоречат друг другу (30 s против 10 s) | считать признаком живости кадры, полученные reader'ом: atomics `lastFrameAt`/`delivering`; если кадр ждёт доставки или пришёл за последний interval, ping не проваливать (или откладывать). Либо дать reader'у ограниченный буфер (N кадров / ≤1 MiB), чтобы control-кадры читались | S–M | низкий/средний | проба `TestAuditProbeSlowConsumerFailsLiveness` должна завершаться успехом; существующий тест «upstream stops answering pings» обязан по-прежнему падать |
| NET-3 | P1 | reliability | `websocket_sessions.go:194-196` (возврат кешированного `connection`), `:285` (idle 30 min), `responses_websocket.go:31-34` (проба стартует только после отправки create). Проба подтвердила | Простаивающие сокеты молча умирают (NAT/LB idle; у Cloudflare ≈100 s — гипотеза о провайдере). Каждое такое переиспользование = 40 s зависания, проваленный ход, неизвестный usage на сверке и запрет replay, хотя create никуда не дошёл | перед повторным использованием сокета, простаивавшего дольше 20–30 s, делать `Ping` с таймаутом 2–3 s под арендой; при ошибке — discard и новый dial (pre-create, `RejectedBeforeExecution`). Опционально лёгкий idle-ping раз в 30–60 s и срок удержания по реальным данным провайдера | S | низкий (+≤1 RTT после долгого простоя) | проба `TestAuditProbeDeadIdleSocketReuse`: второй запрос успешен через новый handshake |
| NET-4 | P1 | perf (вне зоны: application/sqlite) | `application/proxy.go:282` (`ReserveUsage`) и `:353` (`affinity.save`) до dispatch; `:388` (`settle`), `:412` (`RecordAccountOutcome`), `:454` (`remember`) до `output.finish()` (`:423/459/467/475`). strace: 1,04 + 4,06 fsync на запрос | TTFT +4,5–8 ms на NVMe (на сетевом блочном диске VPS больше); `response.completed` приходит клиенту через ≈ 25–37 ms после upstream. Каждый ход tool-loop в Codex платит это время. Транспорт добавляет 0,1 ms | объединить пост-терминальные записи (settle+outcome+remember) в одну транзакцию → 1 fsync; сохранение affinity перенести в транзакцию резерва; отдавать терминал сразу после единственного коммита. Отдельное решение администратора: `synchronous=NORMAL` для не-ledger данных | M | средний (инварианты учёта) | E2E-бенчмарк из scratch: на NVMe `terminalhop_p50` с ~31–38 до ≲8 ms; strace — 5 → 2 fsync на запрос |
| NET-5 | P1 | perf | httpapi: `proxy.go:171`; `proxy_websocket.go:89,153,160`. provider: `provider.go:331` (`requestModelAndStream`); `chatgpt_request.go:69,83,118`. upstream: `request.go:34,38`; `adapter.go:121` (`items()`); `request.go:274` (`containsImageInput` вычисляется даже при `ImageInput=true`); `adapter.go:331,345`; `responses_websocket.go:217-229`. Замеры выше | ≈15 полных сканов/перекодирований тела. На 1 MiB: 35–50 ms CPU на i9 (на vCPU VPS в 2–3 раза больше), 37–55 MB мусора, TTFT 70–109 ms. Под нагрузкой это CPU-bound очередь (ср. context.md: 1 MiB × 256 → TTFT 16–18 s) | разбирать тело один раз на границе в упорядоченный top-level `wireObject` (ключ → `RawMessage`); передавать его вниз, менять только top-level поля, сериализовать один раз сырой конкатенацией (без повторного compact и HTML-escape уже валидных значений). Быстрые шаги: условие `!cap.ImageInput && containsImageInput(items)` и пропуск `req.items()` для passthrough; `websocketResponseCreate` собирать из карты `passthroughBody`; в WS-reader не перепарсивать неизменный `signal.Payload` | M–L | средний (меняется побайтовый wire и порядок ключей) | микробенчмарк: 1 MiB 18,8 → ≤4 ms; golden-тесты семантического равенства upstream-тел; полный suite |
| NET-6 | P2 | perf (эксперимент) | замеры: E2E 1 MiB TTFT SSE 73→25 ms, WS 109→35 ms; колбэк на событие 3364→2451 ns; тесты transport-пакетов зелёные | крупный выигрыш без правок кода | собирать с `GOEXPERIMENT=jsonv2` (Makefile/Dockerfile) после полного `go test -race ./...` и ревью кода, зависящего от текста ошибок json (например, `"invalid upstream JSON: "+err.Error()`) | S | средний (экспериментальная фича Go 1.26, edge-case семантики v1-compat) | полный suite + race + `scripts/compare-runtime.py` |
| NET-7 | P2 | perf | `upstream/responses_stream.go:38,69,74`; `responses_websocket.go:141,155,195,203`; `sse.go:31` (копия №1); `adapter.go:315-327` (`json.Valid` + `NewDecoder` + 2×`Decode`) | 3,4 µs и 36 allocs на событие против 1,3 µs и 8 allocs; WS 2,9 → 1,2 µs. При 512 потоках × 50 ev/s это заметная доля «52% CPU» из context.md | декодировать только заголовок `struct{Type string; Error json.RawMessage}` через `json.Unmarshal` (тот же контракт: валидность + запрет хвоста), `response.id` — только для created/terminal; убрать вторую копию (`readSSE` и `conn.Read` уже отдают свой слайс) | S | низкий | тесты terminal/invalid stream; микробенчмарк |
| NET-8 | P2 | perf/simplification | `httpapi/proxy_sse.go:44,145-151`; `proxy_websocket_stream.go:49,64` | `writeSSE` 747 ns/3 allocs; rendezvous+ack 530 ns/2 allocs и 2 переключения горутин на событие; по syscall на событие даже во всплесках | `json.Compact` только если есть `\r`/`\n`; append в переиспользуемый буфер и один `Write`. Писать из worker под mutex (единственный writer вместе с heartbeat) или буферизованный канал без ack с flush при пустой очереди; ошибку записи передавать через cancel | S–M | низкий/средний (single-writer для heartbeat сохранить) | тест «Ready consumer receives a burst», heartbeat-тесты, ns/event |
| NET-9 | P2 | reliability | `cmd/codex-lb/runtime.go:67-74`: нет `HTTP2: &http.HTTP2Config{SendPingTimeout, PingTimeout}` при `ForceAttemptHTTP2: true`; в SSE-чтении нет idle-границы; `ResponseTimeout` = 2 h (`application/proxy.go:100`) | у SSE нет аналога WS-ping. Мёртвое h2-соединение (все потоки к chatgpt.com на 1–2 TCP — гипотеза, что согласуется h2) ловится только TCP keepalive (минуты), новые запросы ждут `ResponseHeaderTimeout` 60 s. По умолчанию h2 держит до 4 MiB буфера на поток при медленном клиенте | `transport.HTTP2 = &http.HTTP2Config{SendPingTimeout: 30s, PingTimeout: 10s, WriteByteTimeout: 30s, MaxReceiveBufferPerStream: 1<<20}` — liveness транспорта, не прогресс модели (как в WS-требовании spec) | S | низкий | httptest TLS+h2 и прокси, который «замораживает» соединение: поток падает ≲40 s; тесты длинного reasoning не меняются |
| NET-10 | P2 | perf/latency | `upstream/responses_stream.go:23,72` (стоп на терминале, `defer Close` без EOF); `chat_stream.go:77-79` (`[DONE]` → `io.EOF`); `provider/native_api.go:251-258`. Проба: 20/20 новых соединений | на HTTP/1.1-провайдерах каждый запрос платит TCP+TLS (2–3 RTT, полный TLS — см. NET-16) прямо в TTFT; на h2 — RST_STREAM на каждый запрос | после терминала/`[DONE]` дочитать остаток с ограничением (`io.CopyN(io.Discard, body, 64<<10)` и таймер 100–250 ms, закрывающий body), затем `Close` | S | низкий | проба `TestAuditProbeHTTP1ReuseAfterTerminal` → 1 соединение на 20 запросов |
| NET-11 | P2 | perf (квадратичность) | `upstream/chat_stream.go:182` `existing.Function.Arguments += ...` | большой patch в мелких дельтах: 20k дельт = 153 ms CPU и 1 GB мусора на один tool-call (Z.AI/chat-источники) | аккумулятор `strings.Builder` на вызов (`map[int]*toolCallAcc`) | S | низкий | микробенчмарк; существующие chat tool-тесты |
| NET-12 | P2 | protocol | `chat_stream.go:170-187` (tool-calls только копятся), `:310-381` (`added`/`arguments.delta`/`done` выдаются в `emitTerminal`) | пока генерируется tool-call, клиент не видит ни одного события. Длинный patch может упереться в idle-timeout клиента (у Codex по умолчанию 300 s — гипотеза о конфигурации), плохой UX | выдавать `output_item.added` по первой дельте с id/name и стримить `function_call_arguments.delta`; проверку «объявлен ли tool» делать по первому имени, дубликаты ID — в конце | M | средний (семантика валидации tool) | chat-тест со стримингом tool-call: дельты аргументов идут до `[DONE]` |
| NET-13 | P3 | protocol | `httpapi/native_api.go:339-432`: нет веток `response.incomplete`/`failed`; `chunk()` (`:445-453`) без `created`/`model`; неиспользуемый разбор в `map` (`:340`). `upstream/responses_stream.go:115` ставит `Failed` для не-completed → `native_api.go:226-228` `nativeFailedError` (по коду, тестом не проверял) | OpenAI-совместимый клиент после частичного контента получает error вместо `finish_reason:"length"`; строгим клиентам нужны `created`/`model`. Для ChatGPT редко (там удаляется `max_output_tokens`), для внешних Responses-источников реально | маппинг incomplete(max_output_tokens) → `finish_reason:"length"` + usage + `[DONE]`; добавить `created`/`model`; убрать лишний парс | S | низкий/средний (settle как success with usage) | route-тест `/v1/chat/completions` с заглушкой `response.incomplete` |
| NET-14 | P2 | reliability/memory | семафор `reading` (128) есть только в `httpapi/proxy.go:148-155`; нет его в `native_api.go:53,91`, `codex_operations.go:84` (compact 32 MiB), `:106`, `:130`, `:155/170` (25 MB audio), `:191` | обход ограничения памяти (бюджет 500–600 MB): всплеск auto-compaction (полный контекст) или транскрипций читается без лимита | общий body-семафор для всех proxy-маршрутов (лучше взвешенный по байтам) | S | низкий | тест: 129-й параллельный медленный compact → 503 `local_capacity_exceeded` |
| NET-15 | P2 | protocol (гипотеза) | `httpapi/proxy_sse.go:120-133` шлёт `: keepalive` (SSE-комментарий), а prelude придерживает `created`/`in_progress`; для WS ровно поэтому сделаны прикладные heartbeat (context.md, «WebSocket liveness») | SSE-парсер Codex (eventsource) не выдаёт комментарии как события, поэтому idle-таймер клиента не сбрасывается: долгое «тихое» reasoning по HTTP/SSE рвётся с последующими ретраями — гипотеза о поведении CLI | на нативном `/backend-api/codex/responses` (SSE) слать `event: response.in_progress` с реальным ID или `codex.keepalive`, как в WS; для `/v1` оставить комментарии | S | низкий/средний | тест с тихим upstream > idle-timeout; авторизованная проверка реальным CLI |
| NET-16 | P3 | perf | `cmd/codex-lb/runtime.go:67-74`: `TLSClientConfig` nil → нет `ClientSessionCache` | каждый WS dial (h1-only, `requiresHTTP1`) и каждое новое h1-соединение — полный TLS-handshake (CPU на проверку цепочки сертификатов на 1 vCPU) | `TLSClientConfig: &tls.Config{ClientSessionCache: tls.NewLRUClientSessionCache(512)}` | S | низкий | httptest TLS: `ConnectionState().DidResume` на повторных dial |
| NET-17 | P3 | memory | `upstream/sse.go:23` (`make([]byte, 64<<10)` на поток), `:31` (+ `bytes.Buffer`): событие копируется трижды | 512 SSE-потоков → 32 MiB только стартовых буферов; событие 16 MiB (картинка) → ~48 MiB пика | стартовый буфер 4–8 KiB (Scanner растёт сам); одиночную `data:`-строку выдавать без промежуточного буфера | S | низкий | B/op в `readSSE`-бенчмарке, RSS на 512 потоках |
| NET-18 | P3 | reliability (гипотеза о частоте) | `sse.go:12` `maxSSEEvents=100000` применяется и к WS (`responses_websocket.go:82`, `responses_stream.go:31`) | очень длинный оплачиваемый ответ (мелкие дельты) обрывается после выполнения как `stream_incomplete` → неизвестный usage | ограничивать по байтам/времени (размер события и 2 h deadline уже ограничены) или поднять до ≥1M | S | низкий | тест со 150k мелких дельт |
| NET-19 | P3 | protocol | `httpapi/proxy_websocket_stream.go:33-38,83-101`; `proxy_sse.go:58,120` — тикер не сбрасывается реальными записями | `response.in_progress` вклинивается между дельтами раз в 10 s; лишний трафик | сбрасывать тикер (или хранить `lastWrite`) на каждой реальной записи; heartbeat только после 10 s тишины | S | низкий | heartbeat-тесты + тест «непрерывный поток без heartbeat» |
| NET-20 | P3 | latent bug/simplification | `httpapi/proxy_websocket.go:87-89` → `:153` (повторный `Unmarshal` в тот же непустой map: ключи, удалённые `PrepareCapability`, воскресают), `:160` (`json.Marshal` сортирует ключи и HTML-escape'ит всё тело, подтверждено: `<` → `<`), двойная аутентификация `:78` и `:209` | сегодня не эксплуатируется (strip меняет `client_metadata` на месте), но это ловушка; плюс 2 лишних полных прохода и раздувание тела | новый map или пропуск перепарса при неизменном payload; одна проверка ключа на admission; финальное тело собирать один раз (NET-5) | S | низкий | unit-тест: поле, удалённое из payload, не попадает upstream |
| NET-21 | P3 | simplification | три разных pump: `proxy_sse.go` (`stream`), `proxy_websocket_stream.go` (`respondWebSocket`), `native_api.go:124-249` (прямая запись и лишний `writeMu`); мёртвые параметры `_ = catalog` (`responses_stream.go:12`, `responses_websocket.go:13`, `adapter.go:142`), `_ = r` (`codex_operations.go:270`); `CodexOperationsHandler.ServeHTTP` строит `ServeMux` на каждый запрос (`codex_operations.go:64-68`); неиспользуемый в проде `ProxyHandler.mux` (`proxy.go:40-42`); `publicWarmupKey` дублирует `authenticateBearerKey` (`public_warmup.go:80`); `websocketURL` избыточен — coder/websocket принимает http(s) (`responses_websocket.go:232`) | три копии семантики ack/heartbeat/ошибок расходятся; мёртвый код | один `streamPump` (single writer, heartbeat, join), удалить мёртвые параметры и обёртки | M | низкий | существующие SSE/WS/native-тесты |
| NET-22 | P3 | reliability | `provider/operations_realtime.go:64-107`: `Write(ctx)` без таймаута и без ping в обе стороны; `httpapi/codex_operations.go:231`: realtime `Accept` вне бюджета 4096 сокетов; `upstream/responses_websocket.go:31`: create пишется только под ctx (2 h) | живой, но не читающий клиент держит realtime-pump бесконечно; realtime-сокеты не ограничены | таймаут 30 s на запись (как в Responses WS), protocol ping, общий семафор сокетов; явный write-timeout для `response.create` | S | низкий | тест с «замороженным» читателем |
| NET-23 | P3 | correctness | `httpapi/codex_operations.go:273-285`: `ClientIP` берётся из `r.RemoteAddr` (trusted proxies игнорируются), `UserAgentGroup` — из клиентского заголовка; в Responses — `ResolveIdentity` (`proxy.go:112-122`) | за reverse proxy compact/files/transcribe приписываются IP прокси; отчёты расходятся | прокинуть `trusted` и использовать `ResolveIdentity`; UA-группу выводить как в Responses | S | низкий | тест с `X-Forwarded-For` и trusted CIDR |
| NET-24 | P3 | perf | `httpapi/proxy.go:209`, `codex_operations.go:369` (`io.ReadAll`), `proxy_websocket_frame.go:36-38` (`ReadFrom` с удвоением); профиль: `readWebSocketFrame` ≈ 2,5 MB на 1 MiB | ~2× аллокаций и копий на большом теле | предразмер по `Content-Length` (с ограничением max) / `Grow` | S | низкий | B/op в E2E |
| NET-25 | P3 | reliability (гипотеза) | downstream WS: нет protocol ping к клиенту; прикладные heartbeat только у native Codex (`proxy_websocket_stream.go:34`) | `/v1` WS-клиенты за nginx/CF (idle 60–100 s) могут отваливаться на длинном reasoning | protocol `Ping` раз в 30 s в активном ходе для всех маршрутов (это не прикладное событие, со spec совместимо) | S | низкий | тест: клиент видит ping-кадры; `/v1` не получает прикладных heartbeat |

## Детали по ключевым находкам

**NET-1 — заражение пула.** `websocketSessions.read` читает постоянно, в том числе пока сокет простаивает
с `refs==0`, и кладёт кадр в буфер `frames` ёмкостью 1. При следующей аренде `acquire` возвращает тот же
`entry`, а `streamResponsesWebSocket` берёт из `session.frames` сначала устаревший кадр. Проверки, что
терминал относится к текущему `response.created`, нет (есть только для interrupt). Минимальный фикс —
неблокирующий `select { case f, ok := <-entry.frames: ...discard... default: }` до `return entry` для
переиспользуемого соединения, плюс сверка ID терминала.

**NET-2/NET-3 — liveness.** Проба считает живостью только pong. Pong читает тот же reader, который при
медленном потребителе заблокирован на передаче кадра. Отсюда ложный отказ. Для простаивающих сокетов
проверки нет вовсе, а проба стартует только после отправки create, когда отказ уже нельзя безопасно
повторить. Оба фикса укладываются в модель spec: «pong — это liveness транспорта», и ни один не вводит
отсечку по тишине приложения.

**NET-4 — где на самом деле TTFT.** Перед dispatch: `ReserveUsage` (+ `affinity.save`) — 1 fsync.
После upstream-терминала, до `output.finish()`: `settle`, `RecordAccountOutcome`, `remember` и
сохранение continuation/aliases — 4 fsync. В E2E на tmpfs predispatch падает с 7,9 до 0,9 ms, а terminalhop —
с 31 до 0,9 ms. Это не транспорт, но именно это видит Codex на каждом ходе. `scripts/compare-runtime.py` уже
пишет `before_upstream` и `forward_first`; их стоит выводить в отчёте сравнения.

**NET-5/NET-6 — JSON.** Тело запроса на пути Responses/WS (только transport-код) проходит:
`Unmarshal(shape)` → `requestModelAndStream` → `chatGPTWireBody` (`Unmarshal` в map, `Unmarshal(input → string)`,
`Marshal`) → `decodeResponsesRequest` (`Valid` + `Unmarshal`) → `items()` → `passthroughBody` (`Unmarshal`) →
`websocketResponseCreate` (`Unmarshal` + `Marshal`). На WS-входе ещё `Unmarshal` → `Unmarshal` → `Marshal` в
httpapi. Каждый `json.Marshal(map[string]json.RawMessage)` заново compact'ит значения, сортирует ключи и
HTML-escape'ит строки: тело меняется побайтно и растёт на 5 байт за каждый `<`, `>`, `&`. Семантику и
prompt-cache это не ломает, но стоит CPU и трафика.

## Быстрые победы (S, низкий риск)

1. **NET-1a**: проверять и выбрасывать сокет с ожидающим кадром перед повторной арендой (≈10 строк) и сверять
   ID терминала.
2. **NET-3**: `Ping` с таймаутом 2–3 s перед повторным использованием после >20–30 s простоя; при ошибке —
   новый dial до create.
3. **NET-2**: свежий полученный кадр или кадр, ожидающий доставки, считать живостью и не проваливать по нему пробу.
4. **NET-9**: `http.HTTP2Config{SendPingTimeout: 30s, PingTimeout: 10s, WriteByteTimeout: 30s,
   MaxReceiveBufferPerStream: 1<<20}` в `runtime.go`.
5. **NET-10**: ограниченно дочитывать body после терминала/`[DONE]`.
6. **NET-7**: лёгкий заголовочный декод вместо `decodeExactJSON(map)` и без второй копии
   (−2,1 µs и −28 allocs на событие).
7. **NET-8a**: `writeSSE` без `json.Compact` и `fmt.Fprintf`, когда в данных нет CR/LF (−735 ns и −3 allocs на событие).
8. **NET-11**: `strings.Builder` для аргументов tool-call.
9. **NET-5 (часть)**: `!cap.ImageInput && containsImageInput(items)`, пропуск `items()` для passthrough
   Responses, `websocketResponseCreate` из уже разобранной карты (на 1 MiB ≈ −9 ms).
10. **NET-16/NET-17**: TLS session cache; стартовый буфер Scanner 4–8 KiB.
11. **NET-6** (эксперимент): сборка с `GOEXPERIMENT=jsonv2` после полного race-suite. По E2E это ×2,9 на 1 MiB.

## Что проверено и в порядке

- Flush выполняется после каждого SSE-события (`proxy_sse.go:85`), заголовки SSE (`no-cache`,
  `X-Accel-Buffering: no`) верные; keepalive не отправляется до dispatch, чтобы сохранить HTTP-статус и
  `Retry-After`.
- Сжатие WebSocket выключено с обеих сторон (по умолчанию в coder/websocket) — правильно для латентности и CPU.
- WS-handshake к upstream корректно идёт по HTTP/1.1 (`Request.requiresHTTP1`) через общий transport с
  таймаутами dial 10 s, TLS 10 s и заголовков ответа 60 s. Hijack-соединения не занимают `MaxConnsPerHost`.
- Join горутин: `stream`/`stop`, `respondWebSocket`/`workerDone`, WS-reader/`readerDone`,
  session-readers/`WaitGroup`, probe/`done`, realtime-pump — все пути ждут завершения.
- Тела ответов закрываются на всех путях ошибок (`beginStream`, `do`, dial error).
- Лимиты кадров: downstream 32 MiB, upstream 16 MiB, ленивый body-permit на 4 KiB — соответствуют spec.
