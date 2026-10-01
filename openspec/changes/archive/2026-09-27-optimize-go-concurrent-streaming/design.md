# Убрать задержку Go-прокси под параллельной нагрузкой

- Last edited with skill pack: `0.2.2`

## Title and scope

Снизить TTFT и хвостовые задержки для сотен пользователей без ослабления
финансового учёта, scope/owner checks или durability. Сначала устранить доказанное
ожидание SQLite, затем оценить оставшиеся узкие места при 8/32/128/256 одновременных
запросах. Число зарегистрированных пользователей не приравнивается автоматически
к одновременным генерациям; тестируем обе нагрузки через много независимых ключей.

## Planning anchor

`scripts/compare-runtime.py` → `/v1/chat/completions` → `NativeAPIService.directChat`
→ `Store.ReserveUsage` → provider. До первого upstream event при8 запросах Go
ожидает35.9мс, forwarding первого токена занимает0.2мс. При1 запросе TTFT6.18мс;
контроль tmpfs даёт медиану4.14мс против43.40мс ext4 с тем же бинарником.
`store.go` ограничивает ВСЕ операции одним connection при WAL/FULL; legacy использует
NORMAL. Сравнение с legacy сохраняется как пользовательский ориентир, но основное
сравнение исправления — baseline Go и candidate с одинаковым FULL.

## Connected groups or observed existing logic

- Persistence: `Store.Open/Close`, standalone reads/getters и read-only report
  snapshots делят pool с `transact`, `ReserveUsage`, `SettleUsage` и
  `RecordAccountOutcome`. Write transactions читают актуальные лимиты и fences;
  эти чтения не должны уходить в независимый snapshot. FULL остаётся неизменным.
- Application/transport: native Chat и Responses требуют durable reservation до
  dispatch, settlement до terminal success/health/owner; unknown usage удерживается.
  WebSocket/SSE не нужно менять для достижения первого измеренного улучшения.
- Lifecycle/security: schema ownership проверяется до journal writes; допустимые
  literal paths не становятся URI options. Readers открываются только после
  migration/WAL и закрываются вместе с writer. Импорт/repair остаются serialized.
- UI/API: форма данных и настройка лимитов неизменны; публичная auth/revocation
  проверяется по committed DB, а не по новому кэшу. Отдельных UI controls нет.
- Capacity: исходный constructor default64 streams — дополнительный реальный
  предел независимо от SQLite. Пользователь требует минимум256 реально активных
  длинных потоков; после проверки ресурсов default повышается до256
  с прежней bounded queue128/15s; per-account create/stream/recovery/fair-share
  ограничения сохраняются. Одного subscription account недостаточно для256
  потоков: тест использует40 разрешённых accounts и отдельные ключи.
- Specs: `openspec/specs/go-runtime/spec.md` сохраняет актуальные financial,
  deletion/route/incarnation и continuation contracts; добавляется concurrency
  requirement. Прошлый rewrite archive остаётся историей. Отдельная reverse-doc
  не нужна: failing path, phase measurements и SQL connection lifecycle уже прочитаны.

## Use cases

### 1. Читать committed состояние без ожидания несвязанной записи
validated SQLite installation --open serialized writer and bounded readers--> concurrent database access --read committed snapshot independently of writer--> current policy and source data --reserve atomically on writer--> admitted request

Implementation Logic:
- Сохранить единственный write connection и WAL/FULL. Чистые repository reads
  используют bounded read-only pool; все read-check-write и accounting operations
  остаются одной writer transaction. Не вводить concurrent write transactions,
  deferred stale snapshots, retry-on-busy или key/source caches.
- Per-connection settings применяются при каждом открытии/reconnect, включая FK,
  busy timeout и query-only для readers. Выбирать существующий modernc driver и
  `database/sql`, не новую зависимость. URI параметры строятся только корректно
  escaped способом; unknown/legacy DB не изменяются при неудачном startup.
- Multi-query snapshots для scope/reports сохраняют read transaction, а обычный
  getter видит committed state. Durable revoke/route/limit fence проверяется ещё
  раз в writer admission. Нельзя объявить текущий rollback-safe read частью другой
  транзакции или выполнять DB I/O под account-capacity mutex.
- Readers и writer имеют явное close ownership; partial open failure освобождает
  всё уже созданное. Никаких новых background goroutines для кэша/инвалидации.

Tests:
- description: Задержанная writer transaction не блокирует чистый getter; getter не видит uncommitted mutations, после commit видит новое состояние.
- description: Каждый reader/reconnect readonly и с обязательными pragmas; отменённые readers не удерживают pool; literal path/foreign DB/Close/startup regressions проходят.
- description: Concurrent reservation/settlement, revoked scopes, repricing, deletion/incarnation/route fences сохраняют ровно одно списание и fail-closed outcome.

### 2. Проверить реальную concurrent streaming нагрузку
unchanged baseline and candidate binaries --run identical isolated workloads--> measured phases and resource samples --compare medians tails errors and cleanup--> verified performance result

Implementation Logic:
- Использовать существующий `scripts/compare-runtime.py`, loopback stub и private
  temporary installations. Добавить baseline binary, configurable client concurrency
  до256 и много ключей; benchmark не считывает env/private data установки.
- Измерять p50/p95/p99 TTFT/completion, pre-upstream и first-forward timing, CPU/RSS,
  descriptors/threads и cancel release. Одни и те же payloads, logging/accounting,
  FULL и дисковая FS у baseline/candidate. Несколько alternating runs, не один lucky run.
- Проверить также настоящий Codex Responses path с теми же owner/key/ledger checks;
  native Chat улучшение само по себе не доказывает Responses throughput. Долгие
  overlapping streams проверяют устойчивое число активных пользователей, короткие — churn.
- Не засчитывать ошибки admission/quota/transport как быстрые успешные запросы.
  Показать применённые capacity boundaries и отсутствие held/reservation/resource leaks.
- Actual HTTP test удерживает256 streams после первого события на40 subscription
  accounts; достигает нужного peak без снятия per-account caps, завершает/отменяет
  вызовы с корректным accounting и проверяет bounded overload отдельно. Повышение
  общего default до256 оценивается отдельно от улучшения8/32, где прежний cap64
  не ограничивал измерение.
- Если после read split dominant cost остаётся несколькими последовательными
  durable commits, уточнить измеренный slice до изменения financial workflow;
  не выключать fsync и не переносить обязательный accounting в fire-and-forget.

Tests:
- description: После оптимизации8/32/128 запросов сохраняют верный output/usage и завершение; cancellation не оставляет background work или занятые slots.
- description: Итог показывает улучшение против исходного Go по TTFT и tails при одинаковой durability и не скрывает пределы сравнения с legacy NORMAL.

### 3. Подтвердить несколько независимых записей одним durable commit
queued short write jobs --execute isolated savepoints on one writer--> pending successful jobs --commit WAL once--> durably acknowledged callers

Observed Existing Logic:
После read pool32 parallel metered Responses всё ещё имеютTTFT183мс/p95428мс;
на1Go processor128 длинных потоков с32KiB input достигнуты без ошибок, ноTTFT1.1с.
Все pure reads уже отделены, forwarding0.2мс. Остались отдельные FULL commits
admission/settlement/outcome/affinity. Простой перенос данных вfiles добавит второй
recovery mechanism и не устранит требование durable admission. Group commit
теперь обоснован измеренным узким местом, не вводится ради гипотетической нагрузки.

Implementation Logic:
- Private per-Store writer worker с bounded queue и batch до16 уже ожидающих
  коротких jobs, без sleep/таймера накопления. Используется прежний `s.db` с одним
  connection, который освобождается после batch; остальные обычные transactions,
  direct writes, import/migrations/retention остаются совместимыми и не группируются.
- Первыми подключаются `ReserveUsage`, `settleUsage`, `RecordAccountOutcome`,
  `SaveAffinity`, `MarkReservationUncertain`. Один job callback получает `sqlCtx`
  и `*sql.Tx`; все его SQL reads/checks/mutations используют этот tx. Последний
  UPDATE/check uncertainty становится атомарным внутри callback.
- Outer transaction и SQL callbacks не используют cancel одного клиента:
  `database/sql` иначе откатит wholeTx, а driver sqlite3_interrupt может потерять
  savepoint. Request cancellation проверяется перед callback и после него до
  RELEASE. Каждый job имеет savepoint; обычная ошибка или cancel доRELEASE
  откатывает только его изменения. После RELEASE вызов ждёт definitive commit,
  даже если клиент отменил запрос; нельзя вернуть ctx.Err одновременно с поздней записью.
- Acknowledgement успешных jobs происходит только после успешного FULL Commit.
  При ошибке управления savepoint/утратеTx/panic batch целиком rollback; commit
  failure даёт ошибку всем pending success, без implicit retry. Не продолжать SQL
  в потенциальном autocommit после испорченной транзакции. Panic не раскрывает
  исходное значение. SQL lifetime ограничен отдельным batch context.
- Close атомарно закрывает admission writer jobs, ждёт уже принятые callbacks и
  результаты, останавливает worker, затем закрывает reader/writer DB. Не закрывать
  channel конкурентно send и не держать sql.Conn между batches. Успешный callback
  не наблюдается клиентом до commit, переменные changed/applied не гоняются с caller.
- Reservation/outcome rowid и key/account/generation/route fences не меняются.
  Application продолжает вызывать outcome после confirmed settlement; health
  failure не откатывает уже завершённый финансовый результат предыдущего job.

Tests:
- description: Concurrent reservations одного ключа не превышают budget; middle-job failure/cancel не портит siblings; readers не видят uncommitted results.
- description: Cancel before execution, inside callback и послеRELEASE; outer commit failure, broken savepoint, callback panic, Close сqueued jobs не дают false success или зависших callers.
- description: Реальный Responses path не вызывает upstream до durable reservation и не выдаёт terminal success до settled accounting; unknown billing, quota/incarnation/route fences проходят прежние регрессии.

### 4. Не удерживать лишние копии большого контекста в активных потоках
validated request context --prepare provider request without redundant retained copies--> active upstream stream --settle and preserve safe continuation--> completed request with released buffers

Observed Existing Logic:
- Actual binary с 256 клиентами, 256 KiB input и 5-секундными потоками достиг
  931.69 MiB RSS и только 128 активных upstream-запросов. `openRuntime` оставляет
  `MaxConnsPerHost=128` независимо от default admission=256. Это скрытая очередь
  транспорта, а не нехватка аккаунтов или разрешённый предел пользователя.
- Upstream WebSocket session store также ограничен 128 живыми сессиями;
  полный набор из 256 разрешённых независимых разговоров иначе получит 503.
- Application и external Responses-to-Chat adapter повторно разбирают и копируют
  input, а HTTP request bodies могут удерживать serialized payload после отправки.
  Удаление SQLite само по себе не убирает эти буферы.

Implementation Logic:
- Согласовать конечный лимит соединений на host с глобальным default 256;
  согласовать с ним предел живых WebSocket sessions. Сохранить bounded admission,
  per-account/source limits, owner/credential isolation, eviction только idle
  sockets и фоновые timeout.
- Убирать только доказанно ненужное копирование и продлевание lifetime буферов
  в существующем parse/dispatch/continuation пути; стандартные JSON/HTTP средства
  предпочтительнее собственного парсера, spill-хранилища или новой зависимости.
- Сохранить неизменность caller payload, раннюю проверку доверенных сигналов,
  полноценный quota replay, error diagnostics и owner isolation. Continuation
  и финансовые записи остаются durable; нельзя ради RSS терять историю.
- Поля application request могут ссылаться на неизменяемые диапазоны одного
  raw JSON body, найденные через stdlib Decoder/InputOffset. Дубли top-level
  ключей и имена с нестандартным регистром сохраняют прежнюю нормализацию перед
  dispatch; быстрый wire path не должен передать другое значение модели,
  чем проверенное key policy. При plaintext input проверяется строка напрямую,
  без сборки и повторного разбора временного message object.
- Профиль CPU отдельно выявил повторные проверки в `responseInput`: массив
  сначала разбирался как строка, затем как массив, затем каждое уже валидное
  значение снова разбиралось в map. Выбор по JSON kind и проверка первого
  символа уже валидированного элемента убирают эти повторы, не ослабляя
  проверку всего JSON и требование object для каждого input item.
- Responses adapter после проверки capabilities удерживает только нужные
  metadata и исходный wire, а не вторые копии распарсенных input/tools/system/
  instructions. Полный payload по-прежнему передаётся HTTP/WS и сохраняется
  для требуемой диагностики и безопасного продолжения.
- Проверять tiny, 32 KiB, 256 KiB и 1 MiB input на 256 потоках; отдельно
  показывать реальный overlap, временный peak памяти и CPU одного процесса.
  Большая память допустима, но результат с неполным overlap не засчитывается.

Tests:
- description: Actual-runtime transport допускает 256 соединений к одному разрешённому upstream, без скрытой очереди на 128.
- description: 256 независимых upstream WebSocket потоков одновременно активны; 257-й отказывает без закрытия активного соседа, а завершение/Close сохраняют очистку.
- description: Регрессии parse/capability, external translation, continuation replay и отмены остаются passing; большие поля и Unicode передаются без потерь, caller payload не мутирует.
- description: Дубли model, escaped spelling того же ключа и Model/model не меняют нормализацию provider wire или применённую key policy.

## Implementation checklist

1. [x] Локализовать задержку и сохранить неизменённый baseline; согласовать границы и update существующего go-runtime contract.
2. [x] Реализовать WAL readers/writer разделение и regression tests без изменения финансовых гарантий.
3. [x] Выполнить concurrent benchmarks и устранить оставшееся подтверждённое blocking место в согласованном scope.
4. [x] Прогнать Go/race/vet, real-binary API checks; синхронизировать specs и итоговые измерения.

## Open questions

Нет блокирующих. ЦелеваяVM:1vCPU/1GB;128MiB — желательный бюджет сервиса,
пользователь явно разрешил больше памяти при необходимости. Основной workload —
длинные потоки, минимум256 реально активных, а не находящихся в очереди. Снижение
параллельности крупных контекстов не принимается как основной способ достижения
бюджета. Пользователь разрешил менять архитектуру и хранение. РеальноеVPSне исследуется; локальные
results сGOMAXPROCS1 не объявляются productionSLA. VPS/deploy/commit не входят.

## Decision log

- 2026-09-27: Пользователь отверг завершение сTTFTрегрессией и уточнил нагрузку
  сотен реальных пользователей. SDD уже согласован. Implementation flow активен.
- Проверка полноты включает отмену, shutdown и read-check-write fences; проверка
  согласованности сохраняет FULL и все прежние financial/owner contracts.
- План начат с минимального native WAL разделения; batching/новая СУБД/кэши не
  добавляются заранее. Дальнейший шаг определяется сравнением, а не предпочтением языка.
- После read split остаётся измеренное ожидание durable writer, поэтому добавлен
  UC3 с узким group commit. Дляпамяти/large-input admisssion выполняется отдельный
  focusedзамер; вынос continuationblobs наfiles не принят без доказанного выигрыша.
- Итог: 256 активных HTTP/SSE и WebSocket подтверждены; queue не засчитывается
  как активная работа. Убраны скрытые transport caps 128 и лишние копии/повторные
  разборы input. 965 обычных и race-тестов, vet, 9 real-binary UI contracts проходят.
  Для tiny input достигнут бюджет 128 MiB; для крупных контекстов измеренная
  память выше, как разрешил пользователь. Данные и ограничения приведены в notes.md.
