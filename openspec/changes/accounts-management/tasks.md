## 1. Reads

- [ ] 1.1 SQLite: `Store.ListAccountViews` reads non-deleted accounts with their quotas, credit status, additional quotas, group memberships, lifetime usage totals, quota refusal times and token ciphertexts in one analytics snapshot and a fixed number of statements, and the last 7 days of usage per account from account slices (day grain inside, hour rows at the edges) with raw partial hours, the unfolded tail, the journal and folded legacy hours; the raw evaluation as fallback and oracle; tests for equal statement counts at 9 and 100 accounts and for rollup and raw equality in every fold state.

## 2. HTTP

- [ ] 2.1 `GET /api/accounts` from `ListAccountViews`: every existing field unchanged (compared field by field with the per-account reads for a rich fixture), credentials decrypted only for the returned token fields, `groups`, `usage7d`, `resetCreditsObservedAt` and `shell` added, `Server-Timing`; no provider call; the per-account path removed.

## 3. Dashboard

- [ ] 3.1 `AccountsTable` with toolbar (search, status and group filters, sort modes, Add account), the row columns, inline priority, Pause/Resume and Re-login, and the row menu (Probe, Export auth, Redeem reset credit, Delete).
- [ ] 3.2 `AccountDetailSheet` with alias editing, quota windows and the 7-day chart refetched every 5 minutes only while open, usage labelled "Last 7 days" and "Lifetime", reset credits "available N as of HH:MM" with Check now, token information, groups editor, routing and trusted-access settings; `/accounts?selected=<id>` opens it.
- [ ] 3.3 One poll (`/api/accounts`, 30 s) carrying the shell status; no provider request on load or selection; inline mutations update the row without reloading.
- [ ] 3.4 Locales `en`, `ko`, `zh-CN`; MSW mocks and handler coverage; `accounts-flow` integration test.

## 4. Verification

- [ ] 4.1 Gates: Go build, vet, lint, tests, race; web lint, typecheck, tests, build; OpenSpec strict validation.
- [ ] 4.2 Contract test of `GET /api/accounts` against the built binary; `BenchmarkDashboardPages` for the accounts list at 100 accounts.
