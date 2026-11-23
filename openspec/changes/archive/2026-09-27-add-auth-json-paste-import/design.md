# Импорт auth.json: файл или вставка

- Last edited with skill pack: `0.2.2`

## Title and scope

В существующем импорте аккаунта дать выбор файла или вставки содержимого auth.json. Сохранить единый серверный импорт, ошибки, ограничения размера и защиту секретов.

## Planning anchor

`web/src/features/accounts/components/import-dialog.tsx#ImportDialog` принимает только `File`; `AccountsPage` передаёт его в `importMutation`, а `api.ts#importAccount` отправляет один multipart `auth_json`.

`openspec/specs/go-runtime/spec.md` и `context.md`: amend. Старый `account-import` остаётся legacy reference; backend и его текущие ограничения не меняются. В `specs/` нет отдельного solution spec этого UI.

## Connected groups or observed existing logic

- Форма хранит выбранный файл и не имеет режима вставки. `onImport` возвращает promise; rejection сейчас выходит из обработчика без перехвата.
- `AccountsPage` использует одну mutation с уведомлением и обновлением списка; менять её контракт не требуется.
- Go `importAccount` читает максимум 1 MiB auth JSON и выполняет существующую проверку и зашифрованное сохранение. Frontend не должен создавать второй JSON API или дублировать разбор токенов.
- Переводы находятся в `web/src/i18n/locales/{en,ko,zh-CN}.json`; компонентных тестов ImportDialog ещё нет. Используются существующие Vitest/Testing Library и MSW.

## Use cases

### 1. Импортировать выбранным способом

empty draft --choose file or paste JSON--> credential draft --submit existing import--> success or safe error

Logic Details:
- Файл остаётся режимом по умолчанию. Нативные radio inputs выбирают file/paste; обычная textarea принимает Ctrl+V без автоматического чтения буфера и browser permissions.
- Вставленный текст оборачивается в `File` с именем `auth.json` и MIME `application/json`; дальше используется тот же `onImport` и multipart endpoint. Проверка JSON/токенов остаётся на сервере.
- Пустой ввод не отправляется; общий предел 1 MiB проверяется через File.size до отправки для обоих режимов. Ошибки не включают содержимое документа.
- На время импорта ввод и кнопка отключены; rejection не закрывает форму и не становится unhandled promise. Существующий error prop остаётся источником серверной ошибки.
- Смена режима очищает предыдущий draft. Приватная форма монтируется только при open=true, поэтому закрытие, включая внешнее изменение open, удаляет file/text state. Успех очищает draft до закрытия.

Tests:
- description: Выбор файла передаёт исходный File; вставка передаёт File с точным UTF-8 содержимым, именем и типом через существующий API.
- description: Пустой ввод, busy и превышение 1 MiB не вызывают импорт; отказ сервера сохраняет исправляемый draft без unhandled rejection.
- description: Смена режима и закрытие/повторное открытие не возвращают прежний текст или файл; успешный импорт очищает draft.

## Implementation checklist

1. [x] Зафиксировать существующий путь и границы изменения.
2. [x] Добавить два режима, очистку draft, безопасные ошибки и переводы.
3. [x] Проверить компонент, API multipart, сборку и отдельную браузерную установку с синтетическими данными.
4. [x] Синхронизировать go-runtime spec/context, проверить и архивировать change.

## Open questions

Нет. Рабочий сервис не обновляется и настоящие auth.json/clipboard не читаются при тестировании.

## Decision log

- Использован согласованный SDD. Reverse documentation и отдельный connected map пропущены: путь полностью виден в небольшой форме и неизменяемом API helper. Полный planning review loop не нужен для этой локальной UI-правки.
- Переиспользуется стандартный File, radio и textarea: нет новой зависимости или Clipboard API fallback. Прямое чтение clipboard не требуется пользователю для Ctrl+V.
- Секретный draft существует только в состоянии формы; не добавляются local/session storage, URL-параметры, логи или аналитика. Существующее хранение импортированного аккаунта не меняется.
- Обновление бинарника, миграции и реальные запросы к провайдеру не входят в эту правку.
- Проверено 2026-09-27: 8 тестов формы, полный frontend suite (1223 passed), typecheck, focused lint и сборка прошли. В отдельном браузерном стенде неправильный синтетический JSON вернул безопасную ошибку и сохранил draft; закрытие и повторное открытие очистили его. Рабочая установка и настоящий clipboard не затронуты.
- Требование и контекст синхронизированы в go-runtime; strict validation change/spec и layered validation прошли. Временная тестовая установка удалена после проверки.
