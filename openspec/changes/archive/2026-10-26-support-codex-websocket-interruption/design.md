# Исправление прерывания ответа Codex через WebSocket
- Last edited with skill pack: `0.2.2`

## Title and scope

Устранить отказ `Only response.create and response.cancel are supported` при отправке клиентом `response.interrupt`, сохранив активный ответ, продолжение и его фактический расход. Общая поддержка публичного `response.steer`, новые настройки и развёртывание не входят в исправление.

## Planning anchor

`ProxyHandler.websocket` отклоняет неизвестный control frame. Локальный клиент Codex (`codex-rs/codex-api/src/endpoint/responses_websocket.rs`, `run_websocket_response_stream`) отправляет `response.interrupt` с ID текущего ответа и `mode: discard_partial_items`. Его тест `steer_interrupts_and_drains_websocket` подтверждает последующий явный create с previous_response_id, а не автоматического successor.

Это compatibility slice через ingress, application и upstream. Основной контракт `openspec/specs/go-runtime/spec.md` дополняется; существующие правила ownership, accounting, heartbeat и quota-only migration переиспользуются. Связанных solution specs вне lifecycle в `specs/` не найдено.

## Connected groups or observed existing logic

- Ingress: `proxy_websocket.go` проверяет ключ для каждого кадра, содержит один active create и один queued create; `proxy_websocket_stream.go` передаёт события и heartbeat. Reader и worker синхронизированы, сеть выполняется вне mutex.
- Application: `ResponseOptions`, `ResponseTarget` и `Proxy.Respond` задают область одного admitted/reserved запроса. `OnResponseID` наблюдает реальный ID до буферизации. Terminal проходит settlement и ownership до публикации клиенту.
- Provider: `respondChatGPT` выбирает subscription WS; external sources не обещают private Codex interrupt. На них control будет локально отклонён без остановки ответа.
- Upstream: `streamResponsesWebSocket` владеет эксклюзивно leased socket до terminal. Его reader pump и existing liveness probe переиспользуются; pool не расширяется.
- Client: Cancel требует matching interrupt ACK, Drain читает до terminal. Generic error прерывает клиентский поток, а structured control failure сохраняет возможность drain. Админка/схема БД не меняются.
- Reverse documentation skipped: focused inspection establishes the current lifecycle sufficiently; connected mapping is recorded here, not in a separate artifact.

## Use cases

### 1. Передать прерывание текущему ответу
валидный control --проверить текущий ответ соединения--> выбранный active request --передать control в leased socket--> upstream acknowledgement --дождаться terminal--> учтённое продолжение

Implementation Logic:
- Upstream регистрирует callback `OnInterruptReady(chan<- string)` с bounded каналом ID ёмкостью 1, только для ChatGPT WebSocket. Callback передаётся через application и provider без глобального registry.
- Downstream reader проверяет response_id и mode, сверяет ID с наблюдаемым текущим ответом и отправляет ID в канал под коротким mutex. Передача nonblocking, сетевой I/O вне mutex. Чужой/неактивный ID и неподдерживаемый transport получают structured failure.
- Upstream loop сериализует create и interrupt в одном владельце socket, повторно сверяет ID и допускает не более одного отправленного interrupt на response. Wire кадр строится из проверенного ID и фиксированного mode; пользовательские лишние поля не копируются.
- ACK обрабатывается до generic error-проверки, поэтому вложенная ошибка response.interrupt.failed не уничтожает текущий response. Внешние provider ошибки не замалчиваются.
- OnResponseID в downstream оборачивает прежний callback, не заменяя heartbeat. В конце attempt ID очищается.

Files And Functions:
- existing: internal/adapters/httpapi/proxy_websocket.go#websocket - read/auth/control dispatch
- existing: internal/adapters/httpapi/proxy_websocket_stream.go#respondWebSocket - response-ID observation
- existing: internal/application/proxy.go#Respond - request-scoped port propagation
- existing: internal/application/response_ports.go#ResponseTarget - control registration boundary
- existing: internal/adapters/provider/provider.go#respondChatGPT - native-only propagation
- existing: internal/adapters/upstream/responses_websocket.go#streamResponsesWebSocket - exact-socket relay

Tests:
- description: Active interrupt reaches the same synthetic upstream socket, ACK/output interruption/terminal reach the client, actual usage settles once and follow-up create continues the same response.
- description: Foreign IDs, invalid mode, revoked key and external sources never interrupt another response; existing cancel still releases capacity.

### 2. Завершить ответ при гонке с прерыванием
terminal --закрыть приём новых control--> завершение response --разрешить queued и sent control--> terminal без остаточных ACK в пуле

Implementation Logic:
- При получении terminal upstream снимает регистрацию control под ingress mutex, затем отклоняет оставшийся queued-but-unsent ID. Таким образом reader не может положить новый ID после drain.
- Для sent-but-unacknowledged control terminal сохраняется; после matching ACK публикуется terminal и socket возвращается в pool. Поздний local control получает failed без доступа к pool.
- Если ACK не пришёл за пять секунд после terminal, socket retires; downstream получает structured failure и terminal, а ledger сохраняет уже известный расход. Ошибка/отмена во время этого ожидания также возвращает известный Result вместо пустого.
- Для законченного ответа не синтезируется accepted: это скрывало бы неподтверждённую upstream операцию. Никаких replay, quota refusal или новых usage reservations от control.

Tests:
- description: Terminal-before-ACK preserves actual usage, delivers matching ACK, and leaves no stale frame for the next create.
- description: Terminal wins against queued interruption; queued command is rejected and never reaches a reused upstream socket.
- description: Missing/mismatched ACK and client disconnect retire only the affected socket while preserving known terminal usage and joining existing worker activity.

## Implementation checklist

1. [x] Реализовать request-scoped control registration, ingress validation и точный upstream relay.
2. [x] Покрыть ACK/terminal races, изоляцию, cleanup, сохранение usage и follow-up существующими Go fixtures.
3. [x] Выполнить focused tests, go test ./..., go test -race ./... и go vet ./...; синхронизировать OpenSpec/context и архивировать проверенное изменение.

## Open questions

Блокирующих вопросов нет. Реальный private upstream в этой задаче не вызывается; контракт проверяется по исходникам клиента и локальному WebSocket stub.

## Decision log

- 2026-10-26: Реализация разрешена запросом «исправляй»; коммит, релиз и обновление сервиса не разрешены этой задачей.
- 2026-10-26: Начальная гипотеза response.steer заменена подтверждённым клиентским response.interrupt; автоматические successors не добавляются.
- 2026-10-26: Выбран relay вместо context cancellation или фиктивного acceptance, чтобы сохранить terminal usage и downstream continuation.
- 2026-10-26: Риск позднего ACK ограничен пятью секундами и retire socket; новое соединение не наследует control предыдущего response.
- 2026-10-26: Проверки полноты и согласованности пройдены после уточнения протокола по клиенту; структурный валидатор прошёл без замечаний. Workflow-оркестрация не используется.
- 2026-10-26: Реализация сверена с обоими use cases и требованиями; блокирующих расхождений не найдено. Route/provider loopback, ACK/terminal races, queued/foreign/duplicate controls, сохранение usage, continuation и прежняя отмена проверены, включая focused race. `go test ./...` и `go test -race ./...`: по 1391 тесту в 15 пакетах; `go vet ./...` пройден. После дополнения только теста malformed controls его targeted race и общий vet повторно пройдены.
- 2026-10-26: Основные go-runtime spec/context синхронизированы. Strict validation изменения и go-runtime, структурная проверка design и diff-check пройдены. Общая strict OpenSpec validation: 39 passed, 22 failed из-за ранее существующих замечаний вне изменённой спецификации; чужие документы не исправлялись. Платные provider-запросы, коммит, публикация и обновление сервисов не выполнялись.
