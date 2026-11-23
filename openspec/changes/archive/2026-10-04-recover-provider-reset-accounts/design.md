# Восстановление аккаунта после внешнего сброса квоты
- Last edited with skill pack: `0.2.2`

## Title and scope

Убрать застрявший quota block после ручного сброса у провайдера, включая аккаунты с уже сохранёнными 100% остатка. Используется обычный usage polling без новых provider requests или изменения базы.

## Planning anchor

`AccountUsageService.refreshAccountUsage` сохраняет восстановленные квоты, но `quotaResetAvailable` снимает блок только при переходе на новый reset deadline. ChatGPT parser отбрасывает явные `rate_limit.allowed` и `limit_reached`, поэтому восстановление до прежнего срока не распознаётся.

`openspec/specs/go-runtime/spec.md` amend: дополнительное основание recovery при сохранении ownership/policy правил. `usage-refresh-policy` reuse как исторический upstream context, без переписывания legacy scheduler. Отдельная reverse-documentation не нужна: путь установлен прямым чтением parser/service/store. Локальный Codex backend model `RateLimitStatusDetails` и tests `rate_limits.rs` подтверждают nested bool shape; исторический incident `2026-06-02-clamp-selector-retry-hint` наблюдал те же поля на `/wham/usage`.

## Connected groups or observed existing logic

- Boundary: `chatgpt/usage.go#rateLimitPayload` и `snapshot` преобразуют provider JSON в `application.UsageSnapshot`; optional bool нужны, чтобы отсутствие не становилось разрешением.
- Orchestration: `account_usage.go#refreshAccountUsage` захватывает outcome перед fetch, проверяет generation/identity, затем сохраняет snapshot. Непринятый snapshot не может восстанавливать account.
- Shared rule: `reset_credits.go#quotaWindowsAvailable` уже проверяет наличие всех известных governing windows, хотя бы одного окна, свежесть и used <100. Переиспользовать её вместе с explicit backend proof; `completeQuotas` дополнительно запрещает отбросить новое неполное окно.
- Persistence: `sqlite/account_outcomes.go#RecoverAccountQuota` уже делает CAS по generation/outcome и допускает только ChatGPT quota-blocked без egress запрета. Новая схема не нужна.
- Consumers: dashboard и selector читают persisted account status; изменять `EffectiveAccountQuotaStatus`, credits и active-owner exception не требуется. UI получит исправленный статус через существующее обновление.
- Validation: parser tests, существующий recovery service fixture и SQLite CAS tests, плюс интеграция refresh → store → account dashboard/selection на локальном stub.

## Use cases

### 1. Получить разрешение провайдера
usage JSON --прочитать optional nested flags--> usage snapshot --проверить identity и сохранить--> принятые свежие квоты

Logic Details:
- `RateLimitAllowed` и `RateLimitReached` — `*bool`; missing/null остаётся nil. Explicit permission требует одновременно true и false соответственно.
- Никаких дополнительных GET, генераций, retry или reset redemption. Используется существующий poll и его captured generation/outcome.

### 2. Восстановить доступность без нового срока сброса
заблокированный аккаунт со свежими квотами --проверить разрешение и все окна--> подтверждённое восстановление --применить CAS--> доступный аккаунт

Logic Details:
- Явный отрицательный flag veto для всего обычного recovery. Если flags не сообщены, сохраняется existing natural-reset branch.
- Для новой ветки требуются оба affirmative flags, `completeQuotas`, и `quotaWindowsAvailable(before, after, refreshStarted)`.
- Пока blocked snapshot не подтверждает все governing windows, пропущенные предыдущие окна не удаляются при сохранении. Иначе повторный неполный poll мог бы потерять само условие completeness и ошибочно разрешить recovery. Полнота также обязательна для natural-reset branch.
- Сравнение old used100→new0 не требуется: следующая успешная observation исправляет и уже застрявшие rows used0.
- CAS применяется после принятого snapshot; новый refusal или administrator policy не перезаписывается. Требования sibling availability не ослабляются ради positive flag.
- Если backend разрешает использование при всё ещё exhausted windows, этот узкий fix не снимает block: это отдельный вопрос admission/credit semantics.

Tests:
- description: Legacy rate_limited и quota_exceeded восстанавливаются при прежнем deadline, без deadline и при уже нулевых previous rows.
- description: Missing/false flags, missing/incomplete/exhausted sibling, newer refusal и operator changes не допускают recovery.
- description: Explicit denial запрещает даже temporal recovery; отсутствие flags сохраняет естественный reset.

## Implementation checklist

1. [x] Уточнить причину, контракт backend flags и связанные existing rules.
2. [x] Передать optional permission flags через parser/snapshot и расширить recovery predicate без новой схемы.
3. [x] Проверить parser, service, guarded storage и видимый status/selection path на deterministic fixtures.
4. [x] Синхронизировать основной Go spec/context, выполнить необходимые тесты и SDD validation.

## Open questions

- Blocking: none. Пользователь разрешил fix через SDD; сервисы не обновляются в рамках разработки.

## Decision log

- 2026-10-04: implementation flow уже активен; новая узкая задача разрешена. Полнота/согласованность проверена root: добавлен veto explicit denial и guard неполного нового окна. Блокирующих вопросов нет; отдельный автоматический review loop не требуется.
- Fresh timestamp сама по себе не является proof доступности; exhausted-to-zero delta тоже не исправляет уже перезаписанные rows. Выбрано явное разрешение backend плюс existing complete-window helper.
- Без новых DB fields, таймеров, API/probes или переписывания routing. Имеющиеся credits/additional-window правила сохраняются.
- Проверено parser → service → реальный SQLite → visible status/selector, повторные неполные polls, refusal/policy guards. Focused recovery 25 tests pass с race; итоговый общий Go/race прогон 1317 passed и vet pass. Контракт/context синхронизирован; real provider calls, commit и deployment не выполнялись.
