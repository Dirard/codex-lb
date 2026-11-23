# Astra pricing scope

This change adds cost estimates for Astra, not a new subscription-credit meter. Sources checked on 2026-09-07:

- [Astra API model](https://developers.openai.com/api/docs/models/gpt-6-astra): USD 10/1/50 for input/cached-input/output; Flex is half standard. The public API describes a >272K surcharge and a 2x Fast rate.
- [Codex / ChatGPT Work pricing](https://learn.chatgpt.com/docs/pricing): Astra's credit rate is 250/25/1250 per million tokens and Codex Fast is 2.5x standard.
- [ChatGPT Rate Card (Enterprise token-based pricing)](https://help.openai.com/en/articles/20001415-chatgpt-rate-card-enterprise-token-based-pricing#gpt-6-astra-codex-long-context-exception), supplied by the administrator and verified in the browser: Astra costs USD 10/1/50 per million tokens, Fast is 2.5x, and the "GPT-6 Astra — Codex long-context exception" explicitly states: "GPT-6 Astra usage in Codex does not incur additional long-context multipliers above 272K input tokens. Codex does not charge for cache writes." The article's billing scope is new Enterprise agreements with USD usage-based billing; codex-lb uses its rates as a cost estimate, not a claim about the administrator's actual subscription invoice.

The built-in Astra estimate follows the Codex rate card (including Fast/priority aliases), not the direct API's long-context and Fast rules. The cited Codex exception and the general API-key pricing statement do not by themselves establish every API-key billing case. This change does not attempt to infer Codex eligibility for arbitrary external API sources.

In codex-lb, subscription-account routing and external model-source routing already have separate cost paths. Configured model-source tariffs remain independent, and an unpriced source is recorded and settled as zero rather than falling back to the built-in model table. This change does not add automatic OpenAI API tariff discovery or modify source pricing. The downstream `/v1` URL alone does not identify the upstream billing mode. Other model entries are deliberately not repriced in this change.

For example, 300K input with 50K cached and 100K output costs USD 7.55 in standard mode, or USD 18.875 in Codex Fast mode, regardless of crossing 272K.

Historical reports use persisted costs, so adding a price-table row alone cannot correct them. Reprice retained subscription logs and apply only their cost differences to persisted summaries; do not rebuild aggregates from an incomplete retained history or resend requests. Already pruned request details cannot be reconstructed. Retrospective reporting must not suddenly exhaust active API-key budgets: existing key counters and reservation settlement records are not rewritten.

No live deployment or production database changes are part of development. The eventual upgrade applies the documented data correction to the target installation.
