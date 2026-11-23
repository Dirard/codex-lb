# Восстановление аккаунта после сброса квоты у провайдера

## Why

После внешнего ручного сброса квоты аккаунт остаётся `rate_limited`/`quota_exceeded`, хотя свежие окна показывают 100% остатка. Текущий recovery распознаёт только сдвиг срока естественного сброса и игнорирует явное разрешение использования от backend.

## What Changes

- Сохранять optional `rate_limit.allowed` и `limit_reached` в полученном usage snapshot.
- Восстанавливать заблокированный аккаунт после свежего явного разрешения backend и проверки всех управляющих окон, даже при прежнем сроке сброса и уже сохранённых нулевых used rows.
- Сохранить CAS по provider outcome и incarnation, защиту operator policy и существующие правила continuation/credits; никаких дополнительных генераций или reset calls.

## Capabilities

### New Capabilities
- Нет.

### Modified Capabilities
- `go-runtime`: обычное usage refresh может снять quota block при явном подтверждении провайдера без изменения reset deadline.

## Impact

ChatGPT usage adapter, application usage snapshot/recovery, существующие regression tests. Без новой схемы БД, UI controls, API routes, зависимостей, изменения запущенного сервиса или автоматического расхода reset credits.
