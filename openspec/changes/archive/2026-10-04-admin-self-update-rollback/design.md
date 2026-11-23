# Обновление и откат установленного codex-lb
- Last edited with skill pack: `0.2.2`

## Title and scope

Кнопки обновления и отката управляют версией программы независимо от systemd, supervisord или Docker. Один поставляемый бинарник содержит непривилегированный управляющий запуск и серверный worker; БД остаётся у одного worker.

## Planning anchor

`internal/adapters/httpapi/server.go#Handler` сейчас отдаёт заглушку `/api/runtime/version` без проверки релиза. `cmd/codex-lb/main.go#run` запускает сервер напрямую; `storage.go#openData` блокирует inode SQLite. Требуется новый жизненный цикл до открытия БД, а не команда перезапуска systemd.

Связанные контракты: `openspec/specs/go-runtime/` reuse (учёт, startup data identity, один SQLite owner); исторические release-management/release-automation reuse только как граница публикации, их Python/Helm pipeline не переносится. Новый контракт — `runtime-updates`. В `specs/` иных solution specs нет. Отдельная reverse-documentation не нужна: текущие entrypoint, lock, shutdown и admin boundary изучены непосредственно.

## Connected groups or observed existing logic

- Запуск: `main.go`, `config.go`, `runtime.go`; текущий `serve` без привязки к supervisor. Нужен parent до `openRuntime`, неизменные listen/data-dir/CLI/env для worker и отдельный приватный управляющий канал.
- Завершение: `lifecycle.go`, `application.Proxy.BeginDrain` и admission; текущий SIGTERM после grace отменяет запросы. Для обновления его разрешено использовать только после отдельной атомарной проверки отсутствия активной работы. Открытые idle WS не равны активной генерации.
- Данные: `storage.go`, `lock_unix.go`, `sqlite/store.go#migrate`, `runtime_identity.go#ValidateRuntime`. Схема сейчас 33; старый бинарник отвергает более новую схему. Самообновление первой версии требует точного совпадения схемы, без миграционного downgrade.
- HTTP: `Server.Handler` уже имеет requireAdmin, CrossOriginProtection и no-store; update actions добавляются только в этот admin mux, не в proxy/key-report routes.
- Интерфейс: `web/src/features/runtime` и Settings. Существующие TanStack Query, ConfirmDialog и локализации используются без новой навигации и библиотек. Read-only key report не монтирует controls.
- Проверки: существующие Go httptest/temporary data fixtures, cmd shutdown/runtime tests, frontend vitest и real-binary integration. Никаких настоящих обновлений или provider calls в тестах.

## Use cases

### 1. Узнать о новой версии
работающий сервер --проверить стабильные релизы--> сохранённый результат проверки --показать состояние--> уведомлённый администратор

Logic Details:
- GET status не выполняет внешний I/O. Parent проверяет GitHub при старте и раз в час; ручная проверка объединяется с текущей, а повторы ограничены. Проверка не зависит от доступности OpenAI.
- Только Dirard/codex-lb, stable go-vMAJOR.MINOR.PATCH, опубликованные полные Linux amd64/arm64 пакеты. Сравнение числовое. Ошибка сети сохраняется отдельно от последнего успешного результата; никогда не маскируется под up-to-date.
- HTTP status содержит currentVersion, latestVersion, updateAvailable, checkedAt, releaseUrl, supported, unavailableReason, previousVersion, canRollback, phase, targetVersion, lastError. Старый runtime/version endpoint сохраняет envelope.

### 2. Подготовить проверенный релиз
подтверждённая версия --загрузить с ограничениями--> архив --проверить целостность и совместимость--> подготовленный исполняемый файл

Input Validation And Contracts:
- Browser передаёт только version; источник, asset и пути выбирает сервер. Redirect допускается только на фиксированные GitHub release/CDN hosts с HTTPS. Нет произвольных URL, команд или заголовков из browser.
- Ограничения: metadata 2 MiB, checksums 64 KiB, archive 128 MiB, extracted executable 256 MiB. Парсер архивов отвергает absolute/traversal paths, symlinks/hardlinks и duplicate binary; запись только нового staging-файла 0600 с последующим chmod 0500.
- После совпадения SHA-256 команда нового бинарника `update-info` с timeout возвращает version, GOOS, GOARCH, schemaVersion, updateProtocol. Совпадение всех полей требуется до переключения; протокол 1 и точная схема 33. Запуск descriptor не получает credentials/environment приложения.

Implementation Logic:
- Проверенные version directories и atomic JSON state хранятся под `<data-dir>/updates`; данные приложения не перемещаются. Initial executable сохраняется как baseline. Parent держит отдельную install lock, worker — существующую DB lock. Рабочие конфигурация и секреты не пишутся в update journal.
- Download/check process является owned task с lifetime parent, а не HTTP request. Одна установка или rollback за раз; error сохраняется безопасным кодом/сообщением.
- Snapshot текущей/предыдущей версии и pending transition переживают перезапуск. Retention ограничен current/previous/pending и pre-switch backup; удаляются только идентифицированные собственные неиспользуемые version directories.
- Digest уже обработанного bootstrap сохраняется отдельно: повторный запуск прежнего файла не отменяет выбор rollback, а новая ручная установка распознаётся по изменившемуся bootstrap.

### 3. Переключить worker
подготовленная версия --дождаться безопасного окна--> закрытый admission --остановить прежний worker--> сохранённые данные --проверить новый worker--> активная новая версия

Logic Details:
- Старый worker продолжает обслуживать клиентов во время загрузки и ожидания idle. Проверка idle и закрытие admission атомарны относительно начала работы. При отсутствии безопасного окна за 10 минут операция завершается отказом; сервер не остаётся draining. Принудительного прерывания активной генерации нет.
- Подготовленное закрытие admission имеет локальную lease 15 секунд и явную отмену. Приватная команда stop фиксирует остановку под тем же mutex; истёкшая lease автоматически открывает admission и не допускает поздней остановки уже занятого worker. Обычный shutdown не отменяется этим механизмом.
- После закрытия admission idle WS могут переподключиться. Это короткий рестарт, не seamless handoff. Последовательность stop/join/DB close выполняется до нового DB owner.
- До переключения сохраняются согласованные DB snapshot и соответствующий encryption.key, с private permissions и без восстановления автоматически. Ошибка backup не допускает запуска новой версии.
- Parent и worker используют private local IPC с ограниченными messages; публичный admin API не даёт прямого доступа к нему. Parent управляет только своим child, без OS service-manager commands.
- Candidate стартует с fenced public admission/background work, проверяет DB/key/listener и подтверждает readiness. Только после подтверждения parent фиксирует новую current/previous пару и открывает обслуживание. Startup timeout — 120 секунд.
- Неудачный candidate останавливается полностью; previous запускается с теми же аргументами и БД. Failed state виден после восстановления. Если восстановление тоже не удалось — явная fatal ошибка, без retry loop и скрытой очистки данных.
- Candidate, которому ещё не разрешали обслуживание, принудительно завершается и join-ится, если игнорирует graceful stop. Для serving worker такая forced ветка в update/rollback запрещена. Проверка hash выполняется до любого исполнения, включая `update-info` при boot. Release discovery сохраняет durable install/recovery failure в статусе.

### 4. Откатить программу
работающая версия с previous --проверить подтверждённый target--> совместимый previous --переключить worker--> прежняя программа на текущих данных

Logic Details:
- Только реально сохранённый previous, не произвольная старая GitHub-версия. Перед использованием повторная проверка digest/descriptor/схемы. После успешного rollback current/previous меняются местами, результат явно помечен.
- Запросы, лимиты, настройки, encryption.key и unknown-usage reservations не восстанавливаются из backup. Несовместимая схема означает unavailable rollback с причиной.
- Restart после прерванного перехода выбирает последнюю durable current и проверяет её; pending не означает успеха. При failed boot используется сохранённый совместимый previous, не любой скачанный файл.

### 5. Показать действия в админке
загруженные Settings --прочитать update status--> доступные действия --подтвердить update или rollback--> наблюдаемый результат

Logic Details:
- Компактный runtime update block в Settings: текущая версия, новая версия и её release link, кнопка check, update и rollback target. Confirmation предупреждает о кратком restart и сохраняемых данных.
- При busy действия disabled. Status polling ускоряется только во время operation; скрытая страница не создаёт лишний polling. Временная недоступность во время restart не считается завершением и не разлогинивает пользователя.
- Unsupported host/read-only/noexec filesystem или отсутствие managed launcher дают причину и ссылку на release, а не неработающую кнопку. В Docker требуется persistent writable executable data mount; host image/container engine не модифицируется.
- Dev build и динамический listen port поддерживают обычное обслуживание с отключённым updater. Direct fallback с noexec разрешён только если bootstrap byte-for-byte совпадает с durable current; при недоступном state иной выбранной версии запуск завершается явной ошибкой, не подменяет её старым bundled executable.

## Implementation checklist

1. [x] Классифицировать задачу, зафиксировать независимость от systemd и границы данных/схемы.
2. [x] Реализовать typed update status, release/descriptor contracts и application coordination.
3. [x] Добавить ограниченный GitHub downloader, digest/archive checks и private durable installation storage.
4. [x] Добавить universal parent/worker lifecycle, readiness, idle gate, backup и failed-start rollback.
5. [x] Подключить защищённые HTTP routes и UI с подтверждением/прогрессом/ошибками.
6. [x] Проверить download rejection, конкурентные операции, active HTTP/WS, restart recovery, сохранность БД и actual-binary update/rollback на локальных fixtures.
7. [x] Выполнить Go/race/vet, frontend lint/typecheck/tests/build, структурную проверку спецификации и синхронизацию основного контракта.

## Open questions

- Blocking product questions: none. Пользователь подтвердил универсальный parent/worker вариант и реализацию; выбран консервативный idle + short restart, без принудительного разрыва генераций.
- Validation tooling: пользователь разрешил изолированное окружение `.local/spec-validator`; зависимости установлены, structural validator и OpenSpec strict прошли.

## Decision log

- 2026-10-04: новое non-trivial lifecycle поведение, SDD подтверждён; implementation flow активен в этом чате до просьбы пользователя остановить автоматическую реализацию. Commit/release/VPS update не разрешены этой задачей.
- Systemd-specific updater отвергнут пользователем. Не добавляются service-manager adapters, sudo rules, Docker socket или отдельный скачиваемый installer.
- Один бинарник, два роли-процесса; только worker открывает SQLite. Parent не проксирует model bodies и не удваивает upstream connections.
- Exact-schema updates вместо автоматического восстановления устаревшей БД; rollback — только program rollback. Будущие миграционные обновления и seamless blue-green явно вне этого slice.
- Connected mapping обязателен (UI → authorization → parent → files/process → SQLite lifecycle). Отдельная reverse-documentation пропущена, поскольку существующий путь подтверждён кодом. Новых зависимостей runtime/frontend нет.
- `design.md` — canonical layered solution spec в требуемом repository OpenSpec location; нормативные требования находятся в delta spec. Доказательные матрицы не создаются.
- Проверка полноты/согласованности: уточнено отличие idle Responses WS от активного realtime/voice. Gate вызывается через приватный listener, не учитывающий собственный запрос как runtime work. Прочих противоречий текущей задаче не обнаружено; новые функции вне scope не добавлены.
- Проверка реализации: исправлены сохранение исходного previous при failed activation, premature retention и отмена потерянного idle-gate acknowledgement. При stop error флаг ожидаемого выхода восстанавливается; Linux создающий OS thread удерживается до завершения child, чтобы Pdeathsig не сработал из-за завершения другого Go thread.
- Runtime UI на изолированном loopback fixture проверен в браузере; fixture остановлен. Реальная 401 сохраняет обычное завершение admin session; reconnect применяется к сети/5xx, а не к отказу аутентификации.
- Одно независимое итоговое review выявило четыре ошибки boot/recovery: зависший fenced candidate, повторное принятие bootstrap после rollback, ранний Inspect до Verify и потерю install failure при discovery. Исправления и узкие регрессии выполнены без нового цикла ревью; дополнительная функциональность не добавлялась.
- Итоговые проверки 2026-10-04: `go test ./...` и `go test -race ./...` — 1317 passed, `go vet ./...` — pass; frontend — 1254 passed / 11 existing skips, lint/typecheck/build — pass. Linux amd64 и arm64 собираются. Локальные actual-binary fixtures проверяют update, restart, rollback, post-upgrade data, failed/hung candidate и tampered boot. Main specs синхронизированы; исторические OpenSpec предупреждения вне затронутых контрактов не изменялись. Git commit/publication и deployment не выполнялись.
