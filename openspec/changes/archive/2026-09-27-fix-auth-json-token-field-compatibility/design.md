# Исправить стандартный auth.json в общем импорте

- Last edited with skill pack: `0.2.2`

## Title and scope

Устранить `Invalid auth.json payload` для стандартных полей Codex как при вставке JSON, так и при выборе файла. Сохранить существующие camelCase aliases и защиту секретов.

## Planning anchor

`internal/application/accounts_service.go#parseAuthFile` читает tokens в struct только с camelCase tags. В то же время `accounts_export.go#codexAuthTokensJSON` уже экспортирует snake_case; существующие HTTP-тесты импорта передавали только camelCase и пропускали эту несовместимость.

Основные `go-runtime/spec.md` и `context.md`: amend. История предыдущего paste-import change остаётся архивом; UI-контракт не меняется.

## Connected groups or observed existing logic

- UI: paste создаёт обычный UTF-8 File, а `api.ts#importAccount` отправляет тот же multipart `auth_json`; байты не преобразуются. Это проверено существующим UI-тестом.
- HTTP: `Server.importAccount` ограничивает размер и вызывает `AccountsService.ImportAccount`; ErrInvalid становится фиксированным безопасным сообщением без содержимого JSON.
- Application: только ImportAccount вызывает parseAuthFile. Далее accountFromTokens применяет account ID/claims и шифрует токены перед persistImportedAccount. OAuth уже создаёт внутренний typed token struct и не нуждается в новых aliases.
- Export: стандартный codexAuthJson использует snake_case. Верхний dashboard tokens object сохраняет camelCase. Оба формата уже существуют; их структура не меняется.
- Storage/routing: миграции и новые правила identity, overwrite, quota или credentials не нужны.

## Use cases

### 1. Импортировать оба поддерживаемых формата

bounded auth JSON --resolve explicit token aliases--> validated typed credentials --apply existing identity and encryption--> imported account

Logic Details:
- В общем парсере читать token object через json.RawMessage и разрешать четыре заданные пары snake_case/camelCase в строковые значения. Не изменять текст credential и не добавлять эвристическое исправление JSON.
- Null/отсутствие не дают значения. После разрешения aliases три обязательных токена остаются непустыми; optional account ID остаётся nullable. Не-строковые значения и разные non-null значения одной пары отклоняются до сохранения.
- Внутренний authFileTokens, HTTP upload, UI, OAuth и export contracts не меняются. Ошибки не включают значения токенов; реальный auth.json пользователя не читается.
- Перед фиксом воспроизвести отказ стандартного multipart на существующем HTTP handler test. После фикса проверить экспорт → импорт без нового account, сохранение token bytes и alias conflicts без изменения хранилища.

Tests:
- description: HTTP multipart со стандартными полями и application tests с camelCase/mixed/equal aliases импортируют одинаковые синтетические данные, включая optional account ID.
- description: Неверные типы, пустые/отсутствующие токены и конфликт aliases возвращают безопасную ошибку без сохранения credential/account.
- description: Собственный codexAuthJson export импортируется обратно в тот же account; existing paste multipart UI regression остаётся зелёной.

## Implementation checklist

1. [x] Проверить parser, callers и оба существующих формата.
2. [x] Воспроизвести ошибку HTTP-тестом и исправить общий parser с регрессиями.
3. [x] Выполнить scoped Go/race/UI проверки и сборку без обновления работающего сервиса.
4. [x] Синхронизировать main spec/context, проверить и архивировать change.

## Open questions

Нет. Автоматическое обновление локального сервиса, реальные provider requests и чтение auth.json пользователя не разрешаются этим исправлением.

## Decision log

- Продолжается согласованный SDD implementation flow. Отдельные reverse-documentation/mapping artifacts и полный planning review loop не нужны для ограниченного parser bug: связанный путь записан здесь, backend auth и хранение не переустраиваются.
- Ponytail: исправление общего parser вместо отдельных конвертеров в file/paste UI; используются стандартные JSON-типы и текущие тесты без новых зависимостей.
- Перед исправлением `TestAccountRoutesImportCRUDAndExportAuth` со snake_case вернул HTTP 400 и то же `Invalid auth.json payload`. После исправления этот multipart regression и application проверки проходят, включая export round trip, null account ID/claim fallback и четыре конфликтующие пары aliases.
- 2026-09-27: полный `go test ./...`, targeted `-race` для account import/export/auth, `go vet ./...` и 8 тестов ImportDialog прошли. Собран бинарник `go-local-e2e-auth-json-import` с ранее проверенными web assets. Main spec/context синхронизированы. Реальные auth.json/clipboard не читались, внешних provider calls и обновления сервиса не было; HTTP-проверка выполнялась на синтетическом handler/SQLite стенде.
