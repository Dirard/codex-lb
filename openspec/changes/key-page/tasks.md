## 1. Reads

- [ ] 1.1 SQLite: period usage of one key by local day, model, user agent and account with latency histograms and the previous period, error codes and the last 3 hours' limit pace from rollups with the journal, the unfolded tail, raw partial hours and folded legacy hours, in one analytics snapshot with a statement count independent of the period; raw evaluation as fallback and oracle; equivalence tests across zones (UTC, Europe/Prague, Asia/Kolkata) and fold states.
- [ ] 1.2 SQLite: recent requests of one key newest first through `idx_usage_events_key_time` with the keyset cursor and pending entries; errors only bounded to 5 000 rows read per call.

## 2. Use case and HTTP

- [ ] 2.1 `KeyPageService` with administrator and owner scopes: period validation, limit projection at the last 3 hours' pace, pool or group remaining, owner visibility and redaction; tests for pace before and after reset, expired windows and no writes.
- [ ] 2.2 `GET /api/api-keys/{id}/page`, `GET /api/api-keys/{id}/requests`, `GET /api/key-reports/page`, `GET /api/key-reports/requests` with parameter validation, firewall, revalidation, no-store caching, compression and `Server-Timing`; security tests (key A cannot read key B, omitted fields absent, hidden upstream quotas, revoked and rotated keys mid-session).

## 3. Dashboard

- [ ] 3.1 `features/key-page` with one `KeyPage` component for `/keys/:id` and the owner mode; period picker in the URL; polling only while the period includes today; owner mode without admin navigation or admin requests.
- [ ] 3.2 Key sign-in keeps `from` and `to`; key links switched to `/keys/:id`; `/keys?selected=` and `/apis?selected=` redirect; APIs detail pane removed.
- [ ] 3.3 Locales `en`, `ko`, `zh-CN`; MSW handlers and coverage; component and route tests.

## 4. Verification

- [ ] 4.1 Gates: Go build, vet, lint, tests, race; web lint, typecheck, tests, build; OpenSpec strict validation.
- [ ] 4.2 Contract tests for the new endpoints; `BenchmarkDashboardPages` for the key page at target scale.
- [ ] 4.3 Release notes listing the deprecation of `GET /api/api-keys/{id}/trends`, `GET /api/api-keys/{id}/usage-7d` and `GET /api/key-reports/reports`.
