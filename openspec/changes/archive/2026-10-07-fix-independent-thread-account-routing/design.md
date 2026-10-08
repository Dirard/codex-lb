# Распределение независимых сабагентов по аккаунтам
- Last edited with skill pack: `0.2.2`

## Title and scope

Новые независимые треды с общим ключом кэша должны выбирать аккаунт по настроенной стратегии, а не наследовать cache pin соседа. Существующее продолжение сохраняет владельца, в том числе при нулевой квоте без upstream quota refusal.

## Planning anchor

`classifyRequestAffinity` выбирает explicit cache hint раньше идентичности треда. `chooseAccount` возвращает здоровый preferred account до weighted/round-robin выборки. Поэтому одинаковый cache key API-ключа способен свести разные Thread-Id к одному аккаунту. Это не проблема физических WebSocket, исправленных в 1.0.6.

`go-runtime` amend: уточнить область cache affinity. Предыдущий parallel-response change и hard-owner continuity reuse. Отдельная reverse-documentation не нужна: источник, потребители и ограничения установлены прямым чтением. Live-инцидент по текущим настройкам не подтверждён: браузерный доступ заблокирован, обходов нет; воспроизводим кодовый механизм на локальных fixtures.

## Connected groups or observed existing logic

- Boundary: `httpapi/proxy.go#responseOptions` читает Session_id/Thread-Id для HTTP и WebSocket. Ancillary compact использует ту же conversationIdentity. Parent metadata и имя роли сабагента не являются доказанной уникальной идентичностью ребёнка.
- Orchestration: `Proxy.Respond` и `CodexOperations.compactAccount` вызывают общий `lookupRequestAffinity` только без hard owner/file owner. `resolveConversation` раньше проверяет previous/session/turn ownership.
- Classification/persistence: `response_affinity.go` строит framed key-scoped SHA-256; `AffinityPromptCache` уже имеет TTL, CAS и reservation fence. Добавить session/thread в значение explicit hint, если ThreadID непуст. Тип, TTL, таблица и список админки остаются прежними.
- Selection: `chooseAccount` и account admission переиспользуются. Не менять веса планов, приоритет burn/preserve, earlier-reset фильтр или лимиты. Round robin нужен для детерминированного теста шести аккаунтов; weighted selection проверяется отдельно без требования равных долей.
- Propagation: provider-facing prompt_cache_key не переписывается. Старые key-wide cache rows остаются для клиентов без Thread-Id; новые named threads не получают fallback к ним. Hard ownership уже работающих тредов не удаляется.
- Validation: shared-helper тесты на scoped keys и границы, HTTP/WS fixture с шестью аккаунтами, compact shared caller, совместимость безымянных клиентов, прежние owner/group/key/quota проверки.

## Use cases

### 1. Выбрать аккаунт нового независимого треда
новый разрешённый запрос --проверить идентичность и cache hint--> scoped soft preference --применить стратегию и admission--> выбранный аккаунт --сохранить reservation-fenced hint--> отправленный запрос

Logic Details:
- При непустом ThreadID explicit hint кодируется отдельным discriminator с SessionID, ThreadID и cacheKey внутри существующего key-scoped hash. Ограничить используемые значения существующим пределом 4096 байт.
- Разные API keys, sessions и threads не совпадают; повтор той же комбинации получает ту же привязку и TTL. Нельзя читать старую глобальную cache binding как fallback.
- Отсутствие ThreadID сохраняет прежний explicit-key контракт, включая работу при StickyThreadsEnabled=false. Пустой/отсутствующий cache key сохраняет старую automatic affinity классификацию.
- Настроенная стратегия выбирает только допустимые аккаунты. Capacity weighted остаётся вероятностной: не обещаем ровно один аккаунт на агента или равные доли шести аккаунтов.

Tests:
- description: Создать шесть независимых Thread-Id с одним process Session_id и одним cache key; при round robin они выбирают шесть аккаунтов через HTTP и WebSocket, сохраняя provider payload.
- description: Unit checks разделяют scoped cache keys, сохраняют TTL/границы/API-key isolation и не принимают старый unscoped pin для нового треда.
- description: Shared compact selection использует ту же классификацию и не наследует cache pin соседнего треда.

### 2. Продолжить диалог без смены владельца
запрос с подтверждённым владельцем --проверить scope и incarnation--> прежний аккаунт --учесть запрос--> сохранённое продолжение

Logic Details:
- Существующие previous-response/session/turn/file aliases имеют приоритет над soft hints. Изменение cache key или создание соседнего треда не даёт права мигрировать владельца.
- Сохранить zero-quota continuation и quota-only cross-account replay. Запрет аккаунта/модели/группы остаётся fail-closed.
- При одинаковых SessionID и ThreadID без иной достоверной идентичности не угадывать, что запрос принадлежит новому агенту. Нельзя использовать меняющийся turn metadata или название роли для создания случайных владельцев.

Tests:
- description: Продолжить каждый из шести тредов с новым cache key и без previous-response delta, затем по явному previous ID; владелец не меняется, в том числе при telemetry 100% used без quota refusal.
- description: Existing anonymous cache affinity и отклонение недоступного hard owner остаются прежними.

## Implementation checklist

1. [x] Проследить общую классификацию, оба вызывающих пути и неизменяемые owner contracts.
2. [x] Воспроизвести объединение независимых Thread-Id и внести минимальное исправление общего classifier.
3. [x] Проверить HTTP/WS, compact, scoped hints и сохранность continuations/учёта.
4. [x] Синхронизировать spec/context, выполнить Go/race/vet и валидацию, затем архивировать проверенное изменение.

## Open questions

- Blocking: none. User разрешил fix, SDD выбран по умолчанию до ответа на optional question. Реальный полный набор настроек сервера не известен; не менять их догадками.
- Настоящий общий previous_response_id или одинаковые session/thread aliases намеренно сохраняют account ownership. Его изменение потребовало бы иной задачи и контракта.
- Отдельный optional вопрос пользователю: оставить строгий earlier-reset bucket или в дальнейшем сделать его мягким предпочтением. Пока сохраняется существующая настройка и её поведение; это не блокирует scoped-cache исправление.

## Decision log

- 2026-10-07: implementation flow активен. Проверка полноты/согласованности root: сохранены anonymous explicit-cache compatibility, operator policies, max hint length, no fallback к старым pins и no live migration. Отдельный автоматический review loop не нужен.
- Выбран scope cache lookup, а не отключение affinity целиком или переписывание provider cache key: это сохраняет cache reuse внутри треда и legacy generic clients без новой настройки/схемы.
- Обновление, коммит, push, релиз и реальные provider probes не входят в этот запрос. Возможный cache miss у нового треда — ожидаемая цена независимого выбора аккаунта; активные треды не перемещаются.
- Red/green: до исправления unit regression объединял anonymous/thread/key scopes и пропускал oversized identities; после исправления обе проверки проходят. HTTP/WS с шестью аккаунтами подтверждают Round robin и Capacity weighted, compact использует тот же scope, continuation и расход сохраняются.
- Для детерминированной weighted проверки выбранному аккаунту дают нулевой subscription weight при сохранённой eligibility через purchased credits и мягких порогах 100%; остальные сохраняют ненулевые веса. Earlier-reset намеренно выключен в fixture, иначе он отдельно сужает pool до единственного известного срока. Это не утверждение о текущих настройках пользователя.
- Одинаковый cacheKey остаётся частью scoped tuple: до установления hard owner разные cache hints могут иметь разные soft pins. Go Thread-Id semantics одинаковы для backend и /v1, поэтому не добавляется угадывание клиента по User-Agent. Legacy responses-api-compat остаётся исторической ссылкой согласно go-runtime/context.md; без Thread-Id сохранён прежний key-wide bounded-cache контракт.
- Итог: `go test ./...`, `go test -race ./...`, `go vet ./...` прошли; дополнительный bare-Session_id compatibility assertion также прошёл с `-race`. Scope/TTL/bounds, HTTP/WS routing, compact и продолжения с telemetry zero проверены на локальных fixtures без внешних provider calls.
- Change, go-runtime spec и layered design валидны. Общий strict spec lint по-прежнему сообщает 22 старых Purpose placeholders в исторических спецификациях; они не входят в этот фикс. Коммит/публикация/обновление сервисов не выполнялись.
