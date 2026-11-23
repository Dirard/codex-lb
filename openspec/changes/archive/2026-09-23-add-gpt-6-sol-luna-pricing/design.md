# Добавить цены GPT-6 Sol и GPT-6 Luna

- Last edited with skill pack: `0.2.2`

## Title and scope

Добавить расчёт стоимости новых запросов GPT-6 Sol и GPT-6 Luna в существующие логи, отчёты и лимиты ключей. Не менять Astra, историю, маршрутизацию или развёртывание.

## Planning anchor

`app/core/usage/pricing.py#get_pricing_for_model` сейчас не распознаёт эти модели. Расширяем таблицу; `_effective_rates` должен применять множитель Fast к ставкам, уже выбранным по длине контекста. `openspec/specs/api-keys/spec.md` и `context.md` дополняются; существующие требования Astra сохраняются.

## Connected groups or observed existing logic

- Расчёт: `get_pricing_for_model` выбирает модель, `_effective_rates` выбирает ставки, `calculate_cost_breakdown_from_usage` считает стоимость с вычитанием кэшированных токенов из обычного входа.
- Потребители: `app/core/usage/logs.py` и `app/modules/api_keys/service.py` уже используют общий калькулятор. Логи и лимиты получают итоговый upstream tier.
- Внешние источники: `app/modules/proxy/api.py` передаёт отдельную стоимость; новой модели в общей таблице недостаточно, чтобы заменить её.
- Особенность: ветка `priority_multiplier` сейчас возвращает результат до обработки длинного контекста. У существующих моделей с этим множителем long-context ставок нет; явные `priority_*` ставки имеют прежний приоритет.
- Проверки: существующие pricing, reservation, proxy API и external-source integration tests расширяются параметрами.

## Use cases

### 1. Рассчитать стоимость Sol или Luna
usage --resolve canonical model--> model price --select context and service tier rates--> effective rates --price uncached input and cached input and output--> request cost

Implementation Logic:
- Добавить две записи `ModelPrice`, точные имена и алиасы `gpt-6-sol-*` / `gpt-6-luna-*`; не добавлять общий `gpt-6*`.
- Перенести существующий выбор long-context ставок перед обработкой tier. Явные `priority_*` ставки и Flex сохраняют прежнюю семантику; `priority_multiplier` применяется к выбранным стандартным ставкам.
- Использовать текущий clamp кэша и нормализацию tier; не вводить новые поля, cache-write расчёт или Batch без соответствующего контракта.
- Новые расчёты автоматически попадают в текущие пути persistence и settlement. Сохранённые суммы старых запросов не пересчитываются.

Data:
- Sol Standard: input/cached/output = USD 2/0.2/10 per 1M; long context = 4/0.4/15.
- Luna Standard: input/cached/output = USD 0.1/0.01/0.5 per 1M; long context = 0.2/0.02/0.75.
- Long context: input strictly greater than 272000; Fast/priority = 2.5x applicable Standard; Flex = 0.5x applicable Standard.

Tests:
- description: Exact, uppercase and snapshot model IDs resolve; unrelated GPT-6 names remain unpriced.
- description: Standard, Fast/priority and Flex below, at and above 272K; cached input is not billed twice.
- description: Proxy completion persists the cost, exposes it in key summaries, and settles the key using the authoritative tier.
- description: Existing Astra no-surcharge and explicit external-source prices remain unchanged.

## Implementation checklist

1. [x] Extend model prices and aliases and compose multiplier-based Fast with long context.
2. [x] Extend existing pricing, reservation, API and external-source tests; run focused checks.
3. [x] Synchronize the main specification/context, validate and archive this change.

## Open questions

None blocking. Historical backfill and release are outside this request.

## Decision log

- 2026-09-23: SDD chosen by the stated default while awaiting the optional preference answer. This is a narrow pricing extension; focused source/caller inspection is sufficient, so separate reverse documentation and connected-code mapping are skipped. No large-task review loop is needed.
- Official sources checked: [Sol](https://developers.openai.com/api/docs/models/gpt-6-sol), [Luna](https://developers.openai.com/api/docs/models/gpt-6-luna), [Codex rates](https://learn.chatgpt.com/docs/pricing), [Codex speed](https://learn.chatgpt.com/docs/agent-configuration/speed), and [USD rate card](https://help.openai.com/en/articles/20001415-chatgpt-rate-card-enterprise-token-based-pricing).
- Model pages explicitly publish the >272K rates; the Codex rate card's explicit exception still names Astra only. Preserve that distinction. Codex Fast is 2.5x, not the direct API's 2x.
- Reuse the existing calculator and tests. Do not copy prior historical migration code or introduce another pricing path.
- Preserve existing microdollar truncation (the 8192/8192 Sol reservation is 98303 microdollars due to float truncation) and persisted `fast` to `priority` normalization; neither is a tariff change.
- Verification: 501 pricing, API-key, source-routing, usage, request-log and report tests passed; Ruff check/format and project-wide ty passed. The change and `api-keys` pass strict OpenSpec validation; all 59 main specs pass ordinary validation. No production state or unrelated worktree changes were modified.
