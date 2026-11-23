# Z.AI: выбор нативного Responses

- Last edited with skill pack: `0.2.2`

## Title and scope

Разрешить нативный Responses в пресете Z.AI, сохранив существующие Chat Completions-источники. Не менять URL, ключи или работающий сервер автоматически.

## Planning anchor

`ModelSourceFormFields` и `POST/PATCH /api/model-sources` сейчас запрещают Responses для `kind=zai`; `sourceCapabilities` дополнительно принудительно подменяет протокол. Это небольшая совместимая правка формы, валидации и dispatch, без новой схемы БД.

`openspec/specs/go-runtime/spec.md` и `context.md`: amend, остальные контракты остаются действующими. В `specs/` нет отдельной спецификации этой возможности; lifecycle-файлы не являются solution specs.

## Connected groups or observed existing logic

- Форма: `model-source-form.ts#modelSourceDraftReducer` сбрасывает Responses при выборе Z.AI; общий `ModelSourceFormFields` блокирует обе protocol-галочки в create/edit.
- API и сохранение: `ModelSourceService.Create/Update` вызывает `ModelSource.Validate`; существующие `Chat` и `Responses` сохраняются в SQLite. Нулевой набор протоколов и неподдерживаемые audio/embeddings остаются ошибками.
- Dispatch: `respondExternal` уже предпочитает Responses при его наличии. Только `sourceCapabilities` отменяет этот выбор для Z.AI. GLM `thinking` нужен существующему Chat-пути, не Responses.
- Совместимость: обычный Chat, native Responses, model mapping, usage, credentials и route-revision fencing уже существуют. Протокол/URL сохраняются явно; угадывание возможностей и автоматический fallback не добавляются.
- Проверки: существующие HTTP CRUD и provider tests; Vitest формы; `go-runtime.test.ts` запускает настоящий бинарник и SQLite с локальным upstream.

## Use cases

### 1. Настроить Z.AI источник

source draft --choose protocol--> configured draft --validate and save--> persisted source

Logic Details:
- Сохранить выбранные Chat/Responses флаги при выборе Z.AI; отключить лишь неподдерживаемые audio/embeddings. Начальная форма по-прежнему использует Chat.
- Разрешить Chat, Responses или оба; хотя бы один обязателен. Существующие поля и API достаточно переиспользовать.
- Администратор сам задаёт URL: для Coding Plan Chat `https://api.z.ai/api/coding/paas/v4`, для Responses `https://api.z.ai/api/v1`. Не дописывать второй `/v1` и не менять endpoint при клике по протоколу.

Tests:
- description: Существующий Z.AI можно изменить с Chat на Responses через форму и HTTP API; создание Responses-only проходит, сохранение не раскрывает ключ.
- description: Audio, embeddings и отсутствие всех протоколов по-прежнему отклоняются.

### 2. Отправить Responses через выбранный источник

validated source and request --select configured protocol--> upstream request --forward response and usage--> settled result

Logic Details:
- Сохранить результат выбора `respondExternal`; применять GLM Chat thinking только при выбранном Chat Completions.
- Переиспользовать native Responses adapter для JSON и SSE, включая инструменты, reasoning, model mapping и billing usage. Не внедрять Chat-поля в Responses и не переключать протокол при ошибке.
- Сохранить существующие capability и credential проверки, изоляцию и правила route revision.

Tests:
- description: Реальный бинарник принимает Z.AI Responses-only source, отправляет native JSON/SSE в локальный upstream и учитывает расход; Chat-совместимость проверяется существующими тестами.
- description: Provider regression проверяет Responses при включённых обоих протоколах и отсутствие Chat-only полей.

## Implementation checklist

1. [x] Зафиксировать ограниченный контракт и связанный код.
2. [x] Исправить валидацию, protocol selection и форму; добавить регрессии.
3. [x] Выполнить focused Go/race/Vitest, сборку и real-binary contract tests.
4. [x] Синхронизировать основной spec/context и завершить OpenSpec change.

## Open questions

Блокирующих вопросов нет. Реальные provider E2E продолжаются отдельно после добавления аккаунтов; эти тесты не расходуют реальные квоты.

## Decision log

- Пользователь разрешил реализацию; применён ранее выбранный SDD. Reverse-documentation пропущена: путь уже однозначен по коду. Connected mapping и layered planning помещены сюда, без отдельных документов.
- Полный planning review loop пропущен: изменение узкое, без новой архитектуры. Проверки структурного формата и поведения остаются обязательными.
- Миграция и автоперезапуск не требуются. Использовать общий адаптер проще, чем новый Z.AI Responses provider.
- Неправильный endpoint останется явной upstream-ошибкой: автоматическая смена URL/протокола могла бы поменять биллинг.
- Источник endpoint-ов: https://docs.z.ai/devpack/quick-start (проверен 2026-09-27).
- Проверено 2026-09-27: новые Go и reducer регрессии падали до исправления; после него прошли `go test ./...`, `go test -race ./...`, `go vet ./...`, 22 Model sources Vitest-теста, focused ESLint и 10 real-binary contract tests. Сборка `go-local-e2e-zai-responses` включает новый frontend.
- В отдельной браузерной установке подтверждено, что после выбора Z.AI сохраняются Responses=true, Chat=false, а Responses остаётся доступным. Временные сервер, вкладка и синтетические данные удалены. Не выполнялись реальные Z.AI запросы, не перезапускался пользовательский сервис и не менялись его данные.
- OpenSpec change и основной go-runtime spec проходят strict validation; design проходит layered validator в существующем `legacy/.venv/bin/python`, без установки зависимостей. Самопроверка по контракту не обнаружила незавершённых сценариев этой правки; полнота реальной provider-совместимости остаётся задачей E2E после добавления аккаунтов.
