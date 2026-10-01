# Общий вход в админку и отчёты по ключу

- Last edited with skill pack: `0.2.2`

## Title and scope

Перенести вход по API-ключу на тот же экран, где находится вход администратора. Сохранить readonly-отчёты, серверную изоляцию и существующие правила admin-auth.

## Planning anchor

`App.tsx` сейчас выводит `/key-reports` вне `AuthGate`, а тот лишь ссылается на отдельную страницу. `KeyReportsPage` содержит и собственную форму, и частную сессию отчётов. Изменяются только UI entry и владение этой сессией.

`openspec/specs/go-runtime/{spec.md,context.md}`: amend. Архив `2026-09-27-add-key-reports-and-account-group-picker` сохраняется как история; backend-контракт не меняется.

## Connected groups or observed existing logic

- Вход: `AuthGate` читает public auth-session, сохраняет bootstrap/TOTP/trusted-header ветки и не должен монтировать admin content до завершения первоначальной проверки. `LoginForm` вызывает существующий password API.
- Key flow: `KeyLogin` проверяет Bearer через первый scoped report, отменяет запрос при unmount и держит draft только в памяти. `ReportSession` владеет отдельным QueryClient и очищает его при выходе.
- Routes: admin layout с навигацией/status/data queries монтируется только через children gate. Для ключа этот layout не нужен.
- Backend/storage: существующий `/v1/usage/reports` остаётся единственной проверкой ключа; cookie, scopes, БД и admin roles не меняются.
- Проверки: существующие AuthGate/LoginForm/portal/route tests и browser smoke на отдельной синтетической установке.

## Use cases

### 1. Войти через общий экран

common sign-in --choose credential method--> credential draft --authenticate selected method--> administrator access or key-scoped reports

Logic Details:
- В AuthGate сохранять key report session как локальное состояние; она не записывается в admin store, URL или router state. Успех key-login показывает lazy KeyReportsPage вместо children, не присваивая admin permissions.
- Существующее оформление входа дополнить нативным выбором administrator/key report. Password/TOTP и уведомление trusted-header сохраняются; bootstrap и явно отключённая авторизация не меняют своих правил.
- Перенести KeyLogin в небольшой auth-компонент, переиспользуя getKeyReport и тот же in-memory session DTO. На смене метода компонент размонтируется: черновик очищается, pending запрос отменяется; admin pending disables method change.
- Страница отчётов получает уже проверенную сессию и onExit, больше не содержит форму входа. Logout/401 возвращают пустую key-форму на общем экране; приватный cache очищается также при unmount.
- Удалить отдельный key-login route; старый `/key-reports` перенаправить на `/`. Общий публичный auth-session read допустим; ни один admin data route не вызывается в key mode.

Tests:
- description: На обычном dashboard entry доступны оба метода; старый bookmark ведёт туда же. Password/TOTP/bootstrap/trusted-header поведение не теряет защиту.
- description: Key-login показывает только его отчёты без admin requests кроме public session; ключ не появляется в storage, URL или внешнем query cache.
- description: Отказ, logout, revocation и смена ключа сохраняют прежнюю изоляцию; переключение метода отменяет pending login и очищает draft.

## Implementation checklist

1. [x] Зафиксировать текущие callers, scope и общий вход.
2. [x] Перенести форму/сессию в общий gate, обновить маршруты и переводы.
3. [x] Проверить регрессии auth/portal/routes, typecheck/lint/build и синтетический браузерный вход.
4. [x] Синхронизировать main spec/context, проверить и архивировать change.

## Open questions

Нет. Деплой, commit/push и реальные provider requests не входят в это уточнение интерфейса.

## Decision log

- Продолжается согласованный SDD implementation flow. Reverse-documentation и отдельный mapping artifact не нужны: затронутый путь уже известен; связанный контекст записан выше. Полный planning review loop пропущен для ограниченного UI-уточнения без изменения backend auth.
- Ponytail: перенести существующую форму и cache, не вводить новый login endpoint, cookie, global credential store, зависимость или multi-tenant admin role.
- При первоначальной проверке AuthGate больше не монтирует admin children до initialized, а password rejection обрабатывается существующей inline-ошибкой без unhandled promise. Это сохраняет разделение двух методов на общем экране.
- Браузерная проверка на отдельном loopback-стенде подтвердила redirect старого адреса, оба метода на одной форме, readonly-отчёт, очистку после logout и обычный вход администратора. Настоящие ключи и установка на 2456 не затронуты.
- 2026-09-27: 30 focused tests, полный frontend suite (1227 passed) и 10 real-binary контрактов прошли; typecheck, targeted ESLint и сборка `go-local-e2e-unified-login` успешны. Strict OpenSpec и layered validation прошли. Main spec/context синхронизированы; тестовый процесс, вкладка и синтетические данные удалены. Backend не менялся, реальные provider calls не выполнялись.
