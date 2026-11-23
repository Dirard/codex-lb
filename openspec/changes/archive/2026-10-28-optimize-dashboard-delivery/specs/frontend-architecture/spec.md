## MODIFIED Requirements

### Requirement: Settings page

The Settings page SHALL include sections for: routing settings (sticky threads,
reset priority, prompt-cache affinity TTL, weekly pace controls, limit warm-up
controls, and Fast Mode prohibition), password management
(setup/change/remove), TOTP management (setup/disable), API key auth toggle,
API key management (table, create, edit, delete, regenerate), and
sticky-session administration. API key create/edit controls that expose
reasoning effort choices MUST include upstream-supported extended efforts such
as `max` and `ultra`.

Advanced sections — routing settings, model sources, firewall, and
sticky-session administration — SHALL render inside an Advanced settings group
that is collapsed by default. Expanding the group SHALL take exactly one
interaction, after which every previously mandated section SHALL be reachable
and fully functional. While the group is collapsed, its sections SHALL NOT
mount, and the sections that self-fetch on mount — model sources, firewall, and
sticky-session administration — SHALL NOT issue their data requests; those
requests fire when the group is expanded. Core sections (appearance, import,
password management, session, TOTP, and API key management) SHALL remain
visible without expanding the group. The Settings page SHALL NOT render upstream
proxy administration, quota phase planner or guest-access controls, which the
Go runtime does not provide.

#### Scenario: Advanced settings collapsed by default

- **WHEN** a user opens the Settings page
- **THEN** appearance, import, and API key management sections are visible
- **AND** the advanced sections (routing, model sources, firewall, sticky sessions) are not mounted
- **AND** the self-fetching sections (model sources, firewall, sticky sessions) have not issued their data requests

#### Scenario: One interaction expands every advanced section

- **WHEN** a user activates the Advanced settings group trigger
- **THEN** the routing, model sources, firewall, and sticky-session sections mount and become fully functional

#### Scenario: API key dialog offers extended reasoning efforts

- **WHEN** an operator opens the API key create or edit dialog
- **THEN** the enforced reasoning control offers `Max` and `Ultra` in addition to existing reasoning efforts

#### Scenario: Save weekly pace gap smoothing window

- **GIVEN** the Advanced settings group is expanded
- **WHEN** a user selects a weekly pace gap smoothing window from the routing settings section
- **THEN** the app calls `PUT /api/settings` with `weeklyPaceSmoothingMinutes`
- **AND** the saved settings response reflects the selected value

#### Scenario: Save prompt-cache affinity TTL

- **GIVEN** the Advanced settings group is expanded
- **WHEN** a user updates the prompt-cache affinity TTL from the routing settings section
- **THEN** the app calls `PUT /api/settings` with the updated TTL and reflects the saved value

#### Scenario: Save staggered idle warm-up setting

- **GIVEN** the Advanced settings group is expanded
- **WHEN** a user toggles staggered idle limit warm-up from the routing settings section
- **THEN** the app calls `PUT /api/settings` with the updated value and reflects the saved value

#### Scenario: Save Fast Mode prohibition

- **GIVEN** the Advanced settings group is expanded
- **WHEN** a user enables or disables the Fast Mode prohibition control in the routing settings section
- **THEN** the app calls `PUT /api/settings` with `prohibitFastMode`
- **AND** reflects the saved value

#### Scenario: View sticky-session mappings

- **GIVEN** the Advanced settings group is expanded
- **WHEN** a user opens the sticky-session section on the Settings page
- **THEN** the app fetches sticky-session entries and displays each mapping's kind, account, timestamps, and stale/expiry state

#### Scenario: Purge stale prompt-cache mappings

- **GIVEN** the Advanced settings group is expanded
- **WHEN** a user requests a stale purge from the sticky-session section
- **THEN** the app calls the sticky-session purge API and refreshes the list afterward

### Requirement: Dashboard projections load after the primary dashboard data

The dashboard SPA SHALL render primary dashboard content from `GET /api/dashboard/overview`
and recent request-log data without waiting for depletion or weekly-credit projection
calculations. Projection-only data, including safe-line depletion markers and weekly-credit
pace, SHALL be available from `GET /api/dashboard/projections` and fetched after overview
data is available. Because projections derive from persisted quota observations, the SPA
SHALL refetch them when the overview `lastSyncAt` value changes, when the operator refreshes
the dashboard manually, or at most every 5 minutes, and SHALL NOT refetch them on every
overview poll.

#### Scenario: Main dashboard renders before projections finish

- **GIVEN** an authenticated operator opens the dashboard
- **WHEN** `GET /api/dashboard/overview` and request-log calls complete before `GET /api/dashboard/projections`
- **THEN** the dashboard renders the primary cards, usage donuts, account list, and request-log surface
- **AND** projection-only safe-line and weekly-credit fields may populate later when the projections response arrives

#### Scenario: Projection endpoint exposes heavy dashboard calculations

- **WHEN** the dashboard client requests `GET /api/dashboard/projections`
- **THEN** the response includes depletion safe-line data and weekly-credit pace data when those calculations are available
- **AND** the overview endpoint does not need to compute those fields for initial page render

#### Scenario: Overview polls without new quota data do not refetch projections

- **GIVEN** the dashboard has loaded projections for the current `lastSyncAt`
- **WHEN** later overview polls return the same `lastSyncAt` within 5 minutes
- **THEN** no further `GET /api/dashboard/projections` request is issued

#### Scenario: New quota observation refreshes projections

- **WHEN** an overview poll returns a newer `lastSyncAt`
- **THEN** the dashboard requests `GET /api/dashboard/projections` once for that value

### Requirement: Dashboard supports runtime locale selection

The dashboard SHALL load translations through `i18next` + `react-i18next`, support at least `en` (default) and `zh-CN` locales, persist the user's selection in `localStorage` under the key `codex-lb-language`, and apply the active locale to the document's `lang` attribute. When no persisted preference exists, the dashboard SHALL detect the browser language and use `zh-CN` for any `zh*` tag and `en` otherwise. Only the English catalogue SHALL be bundled with the entry chunks; other catalogues SHALL be fetched on demand, and the first render SHALL wait until the detected language's catalogue is available.

#### Scenario: First visit with a Chinese browser

- **WHEN** a user opens the dashboard for the first time with `navigator.language = "zh-CN"` and no persisted preference
- **THEN** the in-scope surface (header, status-bar labels, auth screens) renders in Simplified Chinese
- **AND** `localStorage` contains `codex-lb-language=zh-CN`

#### Scenario: First visit with an unsupported browser language

- **WHEN** a user opens the dashboard for the first time with `navigator.language` set to anything that does not start with `zh`
- **THEN** the in-scope surface renders in English
- **AND** the dashboard does not raise locale-loading errors

#### Scenario: User toggles the language

- **WHEN** the user activates the language switcher in the app header and selects `简体中文`
- **THEN** the in-scope surface re-renders in Simplified Chinese without a full page reload
- **AND** `localStorage.codex-lb-language` is set to `zh-CN`
- **AND** `document.documentElement.lang` is set to `zh-CN`

#### Scenario: Selection persists across reloads

- **WHEN** the user reloads the dashboard after selecting a language
- **THEN** the previously selected language is reapplied before the first paint

#### Scenario: English sessions do not download other catalogues

- **WHEN** the dashboard loads with English as the detected language
- **THEN** no Korean or Simplified Chinese catalogue is requested
- **AND** the entry chunks contain no Korean or Simplified Chinese strings

### Requirement: Dashboard serving is compressed, cache-correct, and chart-lazy

The build MUST emit brotli (`.br`) and gzip (`.gz`) variants for compressible text assets of at least 1 KiB, and the Go UI handler MUST serve the best variant the request accepts with non-zero quality (brotli before gzip), with the original `Content-Type`, the variant's `Content-Length`, `Content-Encoding` and `Vary: Accept-Encoding`; otherwise it MUST serve the original bytes. Every UI response MUST carry a strong `ETag` of the bytes sent and honor `If-None-Match`. Variant files MUST NOT be addressable directly. Authenticated admin JSON responses under `/api/` of at least 1 KiB MUST be gzip-compressed when the client accepts gzip, while proxy paths, authentication and key-report session routes, server-sent events and WebSocket upgrades MUST NOT pass through a compressing wrapper. Content-hashed assets under `/assets/`, including fonts, MUST be served with immutable year-long `Cache-Control`; `index.html` MUST remain `no-cache`. Chart vendor code MUST NOT load before first paint: it MUST live in an async-only chunk that is neither statically imported by the entry chunk nor modulepreloaded, and the default `/dashboard` route MUST NOT load it.

#### Scenario: Assets are compressed and immutable

- **WHEN** a browser requests a hashed asset under `/assets/` with `Accept-Encoding: gzip, br`
- **THEN** the response is brotli-encoded with `Vary: Accept-Encoding`
- **AND** carries `Cache-Control: public, max-age=31536000, immutable`

#### Scenario: Plain-HTTP browsers receive gzip

- **WHEN** a browser requests a hashed asset with `Accept-Encoding: gzip, deflate`
- **THEN** the response is gzip-encoded

#### Scenario: index.html stays fresh across deploys

- **WHEN** the SPA shell is requested
- **THEN** the response carries `Cache-Control: no-cache` and an `ETag`
- **AND** a repeated request with that `If-None-Match` value receives `304`

#### Scenario: Admin JSON is compressed

- **WHEN** an authenticated client requests `GET /api/dashboard/overview` with `Accept-Encoding: gzip` and the JSON body is at least 1 KiB
- **THEN** the response is gzip-encoded with `Vary: Accept-Encoding`

#### Scenario: Proxy streaming paths are never compressed by the dashboard wrapper

- **WHEN** a request targets a proxy path (`/backend-api/*`, `/v1/*`, `/api/codex/*`)
- **THEN** the dashboard gzip middleware passes it through untouched

#### Scenario: Ranged asset requests bypass compression

- **WHEN** an asset request carries a `Range` header
- **THEN** the response is served uncompressed with a valid 206 `Content-Range` over unencoded bytes

#### Scenario: Chart vendor code loads lazily

- **WHEN** the built dashboard entry page loads
- **THEN** the recharts chunk is not statically imported by the entry chunk and not modulepreloaded
- **AND** opening `/dashboard` does not request the recharts chunk
- **AND** charts on other routes render correctly once their async chunk loads

### Requirement: Dashboard status separates service readiness from usage synchronization

The fixed dashboard status bar MUST render independent `Service ready` and
`Usage synced` signals. `Service ready` MUST use the existing `/health/ready`
response and MUST treat a failed request or a non-`ok` status as not ready.
`Usage synced` MUST be derived only from the `lastSyncAt` value returned by
`GET /api/dashboard/status`, which MUST equal the dashboard overview
`lastSyncAt` (the latest quota observation of non-deleted ChatGPT accounts)
without computing traffic reports, and MUST be fresh only while that timestamp
is less than 60 seconds old. The status bar MUST NOT request the dashboard
overview. The service-readiness signal MUST NOT use upstream account or
provider health. The dashboard layout MUST reserve at least the status bar's
rendered height so wrapped status rows do not cover page content.

#### Scenario: Ready service with stale usage

- **WHEN** `/health/ready` returns `status: "ok"`
- **AND** `lastSyncAt` is absent or at least 60 seconds old
- **THEN** the status bar shows the service as ready
- **AND** independently shows usage as stale

#### Scenario: Unready service with fresh usage

- **WHEN** `/health/ready` fails or returns a non-`ok` status
- **AND** `lastSyncAt` is less than 60 seconds old
- **THEN** the status bar shows the service as not ready
- **AND** independently shows usage as synced

#### Scenario: Readiness is still being checked

- **WHEN** the initial `/health/ready` request has not completed
- **THEN** the service-readiness signal shows a checking state
- **AND** the usage synchronization signal remains independently derived from
  `lastSyncAt`

#### Scenario: Status signals wrap onto additional rows

- **WHEN** the fixed status bar grows because its signals wrap
- **THEN** the dashboard updates its reserved bottom space to the rendered
  status-bar height
- **AND** the fixed status bar does not cover page content

#### Scenario: Non-dashboard pages do not load the overview

- **WHEN** an operator stays on the Settings page
- **THEN** the status bar refreshes `lastSyncAt` from `GET /api/dashboard/status`
- **AND** no `GET /api/dashboard/overview` request is issued

## ADDED Requirements

### Requirement: Account lists mark duplicate e-mails on the client

The dashboard SHALL derive an `isEmailDuplicate` marker on the client for every
account list it renders (`GET /api/accounts` rows and dashboard overview
accounts), so rows whose visible label is the same e-mail can be told apart.
The marker MUST be `true` when another row of the same list has the same real
e-mail after trimming and case-insensitive comparison, and MUST be `false`
otherwise. Missing, blank, and placeholder e-mails equal to `unknown@example.com`
MUST be excluded from duplicate detection. Rows flagged as duplicates MUST show
a compact account identifier next to the e-mail label. The server is not
required to send this field.

#### Scenario: Duplicate real emails are flagged

- **WHEN** an account list contains two or more rows with the same real non-placeholder email, ignoring case and surrounding whitespace
- **THEN** every row in that group is rendered with its compact account identifier

#### Scenario: Placeholder emails are ignored

- **WHEN** an account list contains two or more rows whose email is blank or `unknown@example.com`
- **THEN** those rows are not flagged as duplicates

#### Scenario: Unique emails are not flagged

- **WHEN** an email appears only once in an account list
- **THEN** that row is rendered without the duplicate account identifier

### Requirement: Account routing-policy control sizes predictably

The account detail routing-policy selector SHALL size itself responsively
within its container instead of using an arbitrary fixed pixel width, and SHALL
truncate long option labels gracefully rather than overflowing its container
or collapsing below a usable minimum width.

#### Scenario: Routing-policy select fills its control row

- **WHEN** the account detail panel renders the routing-policy selector
- **THEN** the selector trigger constrains its width to its container with a usable minimum
- **AND** does not hardcode a fixed `w-44` width

### Requirement: Dashboard API mocks follow the Go router

Every dashboard test mock handler for an `/api/` route MUST correspond to a
route registered by the Go HTTP server with the same method and path, as
resolved by the Go router itself; a mock for a path that Go serves only through
a broader subtree pattern MUST be treated as missing. Mock factories MUST NOT
produce response fields the Go server never sends.

#### Scenario: Mock for an unregistered route fails the build

- **WHEN** a mock handler is added for `GET /api/automations/:id/runs`
- **THEN** the Go route-contract test fails because the Go router resolves that path only to `GET /api/automations/`

## REMOVED Requirements

### Requirement: Account summary duplicate email indicator

**Reason**: The Go accounts API does not send `isEmailDuplicate`, and the marker is a property of each rendered list rather than of an account.

**Migration**: Replaced by "Account lists mark duplicate e-mails on the client".

### Requirement: Request logs distinguish actual and requested service tiers

**Reason**: The Go request-log contract exposes only the billable `serviceTier`; requested and actual tiers are not recorded.

**Migration**: The request-log model label keeps showing `serviceTier`.

### Requirement: API sidebar shows pooled credit bars

**Reason**: The Go API-key list does not compute pooled account credits.

**Migration**: Key limits remain visible through the API limit bar and the per-key usage views.

### Requirement: Request logs expose cost breakdown details

**Reason**: The Go request-log contract exposes token counts and the total `costUsd` only.

**Migration**: The request detail dialog shows the total cost; per-segment cost may return through a new change that adds it to the Go contract first.

### Requirement: Request detail dialog renders successful cost breakdowns

**Reason**: Depends on the removed cost-breakdown fields.

**Migration**: See "Request logs expose cost breakdown details".

### Requirement: Dashboard settings must expose upstream proxy routing controls

**Reason**: `go-runtime` excludes proxy pools.

**Migration**: None; accounts connect to upstream directly.

### Requirement: Dashboard accounts must expose account proxy bindings

**Reason**: `go-runtime` excludes proxy pools.

**Migration**: None.

### Requirement: Upstream proxy admin creation flows use modal dialogs

**Reason**: `go-runtime` excludes proxy pools.

**Migration**: None.

### Requirement: Upstream proxy admin section summarizes configured endpoints and pools

**Reason**: `go-runtime` excludes proxy pools.

**Migration**: None.

### Requirement: Account routing and proxy-binding controls size predictably

**Reason**: The proxy-pool selector no longer exists.

**Migration**: Replaced by "Account routing-policy control sizes predictably".

### Requirement: Account proxy binding can test the selected pool

**Reason**: `go-runtime` excludes proxy pools.

**Migration**: None.

### Requirement: Guest dashboard hides the Conversations view

**Reason**: `go-runtime` excludes guest dashboard access; every authenticated dashboard session is an administrator.

**Migration**: The Conversations view is available to every authenticated session.
