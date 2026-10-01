# Luna для пробы и корректная недельная квота

- Last edited with skill pack: `0.2.2`

## Title and scope

Исправить неподдерживаемую модель ручной пробы и показ единственной недельной квоты как пятичасовой. После проверок обновить локальный экземпляр на 2456 с резервной копией.

## Planning anchor

`WarmupService.executeWarmup` подставляет `gpt-5.4-mini`, а `chatgpt.usagePayload.snapshot` переносит из primary только месячное окно. `SaveAccountUsageSnapshot` делает upsert, поэтому простое исправление parser оставит старый primary в базе.

`go-runtime/spec.md` и `context.md`: amend. Старые archived changes остаются историей; схема БД, правила неизвестного расхода и ранее разрешённые scopes не меняются.

## Connected groups or observed existing logic

- Force probe: AccountsPage не передаёт модель; общий executor использует hardcoded default, выполняет один pinned request и сохраняет известный расход либо pending reconciliation. Пользователь явно выбрал `gpt-6-luna` вместо автоматического выбора другой модели.
- Usage ingestion: FetchUsage переводит длительность в минуты; единственный monthly primary уже переносится отдельно. Для 10080 минут такой обработки нет.
- Application/storage: refresh собирает Quotas и сохраняет их под существующими credential/account/fetch fences. Отсутствующие rows сейчас не удаляются; history хранится отдельно с исходным window_minutes.
- Consumers: accounts/dashboard/routing берут current rows из account_quotas. AccountUsagePanel уже поддерживает weeklyOnly, но legend всегда пишет 5h. Trends/pace используют сохранённые history labels.
- Подтверждённая локальная ошибка: pro account имеет только primary с window_minutes=10080 и used=41. Архив пробы содержит отказ модели gpt-5.4-mini. Единственный pending request принадлежит __admin_warmup__ и не имеет key-limit items; реальные credentials не выводились.

## Use cases

### 1. Запустить дешёвую пробу

authorized pinned probe --apply explicit model or Luna default--> single provider attempt --settle known usage or retain uncertainty--> truthful probe result

Logic Details:
- Изменить только общий default на gpt-6-luna. Явные модели в API, jobs и настройках не переписывать. Не добавлять автоматические model retries или новые catalog restrictions.
- Существующий HTTP-to-provider regression проверяет модель на wire и ровно один вызов, включая ошибки и неизвестный расход. Старый pending reserve не списывается и не удаляется.

Tests:
- description: Admin POST без model отправляет gpt-6-luna один раз, сохраняет pinning и accounting; explicit model остаётся прежней.

### 2. Применить реальный набор окон квоты

provider observation --classify lone weekly window--> complete or partial quota snapshot --commit under existing fences--> correct current windows and weekly display

Logic Details:
- Lone primary с длительностью ровно 604800 секунд переносится в secondary; monthly и обычные два окна сохраняют прежнее поведение. Не угадывать период при отсутствии duration.
- В domain snapshot добавить ReplaceQuotaWindows; application выставляет его только при непустом наборе наблюдаемых standard windows, когда у каждого представленного окна есть used_percent. Пустой/частичный ответ остаётся merge-only.
- В уже существующей транзакции после identity/plan guards проверить полный набор и удалить старые standard rows не новее наблюдения, затем записать актуальные. Некорректный snapshot откатывает всю транзакцию.
- Если полная weekly observation заменяет primary с длительностью 10080 минут, один раз переименовать history rows именно этого account/периода в secondary. Не удалять наблюдения, не менять проценты, timestamps, request usage или чужие данные.
- У weekly-only карточки убрать ложную 5h legend. Существующие UI-условия для основной полосы weeklyOnly переиспользовать; после развёртывания обновить страницу, чтобы сбросить прежнее сглаженное значение.

Tests:
- description: Реальный usage adapter → application → SQLite → HTTP accounts/dashboard переводит primary10080 в единственную secondary и сохраняет 59% remaining.
- description: Partial/absent/stale snapshots сохраняют старые windows; invalid full snapshot rollback и pending identity guards не удаляют актуальные данные.
- description: History trend labels корректируются только у старого семидневного primary, а нормальные 5h/weekly snapshots и месячные окна продолжают работать.
- description: Weekly-only UI с trends не показывает 5h legend.

## Implementation checklist

1. [x] Подтвердить причины и связанный путь без новых provider generations.
2. [x] Исправить default, quota normalization/persistence и legend с регрессиями.
3. [x] Выполнить Go/race/UI/typecheck/build и spec validation, синхронизировать specs.
4. [x] Сохранить rollback, обновить локальный сервис и проверить readiness/версию/реальные quota rows без платной пробы.
5. [x] Зафиксировать результаты и архивировать change.

## Open questions

Нет. Пользователь разрешил исправление и обновление. Автоматическое снятие неизвестного расхода и новая платная проба в проверку не входят.

## Decision log

- Продолжается согласованный SDD; Ponytail: один default и существующий ingestion/transaction/UI, без нового cron, миграционного фреймворка или настроек. Связанный контекст записан здесь вместо отдельного reverse-documentation artifact.
- Проверка полноты выявила необходимость удалить старый current primary, а не только поменять parser. Проверка согласованности закрепила сохранение partial snapshots и неизвестного расхода; новых полномочий не требуется.
- До обновления прошли полный `go test ./...`, targeted race в четырёх пакетах, `go vet ./...`, 35 UI-тестов, typecheck/build и targeted ESLint. Синтетические HTTP-тесты проверяют Luna на wire, один вызов, полный usage → SQLite → accounts/dashboard путь, partial/empty observations, rollback и plan-confirmation fences. Web assets собраны в `go-local-e2e-luna-weekly-quota`; main spec/context синхронизированы.
- 2026-09-27: 10 real-binary контрактов прошли. После разрешённого обновления `codex-lb-go-local.service` на 2456 работает `go-local-e2e-luna-weekly-quota`; readiness/SQLite quick_check успешны. Штатный usage poll записал только secondary10080: 41% used, 59% remaining. 28 исторических семидневных primary labels исправлены; проценты/наблюдения и ключ шифрования сохранены. Старый __admin_warmup__ reserve остался pending с нулём key-limit items. Платная проба не запускалась.
- В браузере после развёртывания карточка показывает только `Weekly remaining 59%`, а график — Weekly/Weekly plan без 5h. Проверочная вкладка закрыта; исходная пользовательская вкладка не перезагружалась. Strict OpenSpec и layered validation прошли; rollback binary/database/key сохранены перед обновлением.
