# Отчёты по ключу и выбор групп аккаунта

- Last edited with skill pack: `0.2.2`

## Title and scope

Дать владельцу API-ключа отдельную read-only страницу его отчётов, а администратору — назначение нескольких групп из карточки аккаунта за одно сохранение.

## Planning anchor

`KeyUsageHandler` уже предоставляет self-service usage по Bearer-ключу, но полные отчёты доступны лишь через admin routes. `App.tsx` сейчас закрывает весь UI через `AuthGate`. `account_group_accounts` уже many-to-many; карточка аккаунта ещё не редактирует эти связи.

Основной `go-runtime/spec.md` и `context.md`: amend. Остальные правила квот, scopes и admin-auth сохраняются. Текущий импорт из буфера — отдельный change `add-auth-json-paste-import`, завершается вместе с общими проверками.

## Connected groups or observed existing logic

- Авторизация: `authenticateProxyKey` имеет keyless-local исключение; self-service reports должны использовать вынесенную строгую Bearer-проверку без него. Internal, inactive, expired и отсутствующие ключи недопустимы; исчерпанный лимит генерации не запрещает чтение отчёта.
- Backend: `ReportsService.Reports` и `QueryReports` уже фильтруют все агрегаты и comparison по APIKeyIDs. Новый обработчик фиксирует единственный ID из проверенного ключа. В ответе разрешены summary/comparison/daily/byModel/byUseragent, но не byAccount/credentials/log payloads.
- UI: новая отдельная route вне admin AuthGate и AppLayout не делает admin/status/accounts запросов. Существующие summary, charts и daily CSV остаются компонентами отображения.
- Credential lifetime: ключ только в памяти страницы и Authorization header, не в URL, storage, query keys или уведомлениях. Приватный QueryClient живёт только в одной key session; logout отменяет запросы и очищает его. Каждая загрузка снова проходит backend auth; ошибка auth скрывает старые данные.
- Группы: `SaveGroup` заменяет состав всей группы и синхронизирует лимиты ключей. Для карточки нужен отдельный узкий SQL update по account_id, который не вызывает SaveGroup и не меняет лимиты/остальных участников.
- Проверки: реальные SQLite HTTP handlers для изоляции двух ключей и транзакций групп; frontend tests и real-binary/browser smoke с синтетическими данными.

## Use cases

### 1. Посмотреть отчёт своего ключа

API key entry --authenticate Bearer--> key-scoped report session --choose dates and load own reports--> read-only report

Logic Details:
- `GET /v1/usage/reports` и trailing slash требуют действительный внешний API-ключ даже при отключённой proxy key auth. Admin cookie не заменяет ключ. Изменяющие методы не регистрируются.
- Сервер принимает только даты, timezone, model и useragent filters; параметры account/key scope отклоняются. Проверенный key.ID передаётся в ReportsService как единственный APIKeyID. Общие ограничения диапазона и размеров фильтров сохраняются.
- Использовать явный allowlist DTO для summary, comparison, daily, byModel, byUseragent, `Cache-Control: no-store`, без аккаунтных идентификаторов/названий и raw request logs.
- `/key-reports` показывает форму ключа, затем сводку, дневные данные/CSV и существующие графики стоимости, токенов и моделей. По умолчанию 7 дней, доступен выбор дат. Никаких кнопок изменения ключей, аккаунтов, лимитов или настроек.
- Ошибка входа не открывает отчёты; logout удаляет credential и report cache. Запросы используют credentials=omit, не создают admin session и не меняют её обработку 401.

Tests:
- description: Два ключа с общим аккаунтом видят только собственные текущие и предыдущие расходы; query override, cookies, keyless-local, internal/expired/revoked ключи не обходят scope.
- description: POST на self-report и admin write с Bearer не даёт write access; чтение не меняет счётчики расхода.
- description: UI login/logout, ошибки и смена ключа не сохраняют ключ в storage/URL/query keys и не показывают данные предыдущей сессии.

### 2. Назначить аккаунту несколько групп

account and current memberships --select groups--> membership draft --validate and commit atomically--> updated memberships

Logic Details:
- В карточке использовать существующий список групп и несколько чекбоксов; Save применяется один раз. Пустой список снимает членство во всех группах, но не удаляет аккаунт/группы.
- Admin-only PUT `/api/accounts/{id}/groups` принимает обязательный массив groupIds; null/отсутствие, дубликаты, пустые и несуществующие IDs отклоняются. Проверка живого аккаунта и всех групп предшествует изменению связей в одной транзакции.
- Удалять/добавлять лишь строки `account_group_accounts` этого аккаунта. Сохранить чужие memberships, group limits, key consumption, reservations и group-to-key bindings. Членство подхватывается существующим live routing.
- UI очищает draft при смене аккаунта, не считает загрузку/ошибку пустым набором групп и оставляет корректируемый draft при отказе. readOnly/busy отключают сохранение.
- Update ограничен 1000 group IDs до построения SQL, чтобы bounded JSON не приводил к превышению SQLite variable limit. После успеха UI ожидает обновление query и сбрасывает draft; reject обработан без unhandled promise.

Tests:
- description: Добавить один аккаунт сразу в две группы, снять одну или все, не меняя другого участника или лимиты/потребление ключей.
- description: Несуществующая группа/аккаунт и недостаточная авторизация оставляют все связи неизменными; frontend посылает один полный update и обновляет список после успеха.

## Implementation checklist

1. [x] Зафиксировать два независимых сценария и security boundaries.
2. [x] Добавить строгий self-report endpoint, read-only страницу и проверки изоляции.
3. [x] Добавить атомарное назначение групп из карточки и регрессии.
4. [x] Выполнить интегрированные проверки, синхронизировать specs и архивировать change.

## Open questions

Нет. Пользователь подтвердил выбор нескольких групп непосредственно в карточке аккаунта. Публичный путь `/key-reports` выбран как отдельная страница; существующая админка не меняет способ входа.

## Decision log

- Продолжается ранее разрешённый SDD implementation flow. Reverse-documentation пропущена: существующие пути ясны; connected context записан здесь. План проверен на соответствие двум пользовательским сценариям без расширения в полноценную multi-tenant админку.
- Для ключа не создаётся парольная/cookie-сессия или refresh token: Bearer проверяется при каждом чтении. TLS остаётся обязанностью внешнего deployment; локальная проверка использует loopback.
- Расчёт отчётов переиспользуется; DTO ограничен явно, чтобы новые поля admin report не публиковались автоматически.
- HTTP-регрессия обнаружила прежний дефект `countReportConversations`: при key/model/useragent фильтре пропускается legacy UNION arm, а raw SELECT не именовал колонку conversation_id. Добавляется alias в общем запросе, чтобы работали те же разрешённые фильтры и в admin reports.
- Изменение групп отделено от SaveGroup, чтобы не затронуть настройки/лимиты остальных сущностей. Схема many-to-many уже достаточна.
- Реальные provider-запросы, commit/push и обновление работающего сервиса не выполняются без отдельного запроса.
- Проверено 2026-09-27: полные Go tests, race и vet; 1223 frontend tests; 10 real-binary контрактов с loopback provider; typecheck, focused lint и сборка `go-local-e2e-key-reports` прошли. HTTP-проверки включают два ключа одного аккаунта и предыдущий период. Браузерный стенд подтвердил login/logout без админки и сохранение двух групп после reload; реальных provider-запросов и обновления установки на 2456 не было.
- Требования и контекст синхронизированы в go-runtime; strict validation change/spec и layered validation прошли. Временный процесс, его вкладка и только синтетические данные удалены после проверки.
