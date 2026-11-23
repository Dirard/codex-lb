## MODIFIED Requirements

### Requirement: Dashboard serving is compressed, cache-correct, and chart-lazy

The build MUST emit brotli (`.br`) and gzip (`.gz`) variants for compressible text assets of at least 1 KiB, and the Go UI handler MUST serve the best variant the request accepts with non-zero quality (brotli before gzip), with the original `Content-Type`, the variant's `Content-Length`, `Content-Encoding` and `Vary: Accept-Encoding`; otherwise it MUST serve the original bytes. Every UI response MUST carry a strong `ETag` of the bytes sent and honor `If-None-Match`. Variant files MUST NOT be addressable directly. Authenticated admin JSON responses under `/api/` of at least 1 KiB MUST be gzip-compressed when the client accepts gzip, and the key owner's page reads `GET /api/key-reports/page` and `GET /api/key-reports/requests` MUST be gzip-compressed in the same way, while proxy paths, authentication routes, the key-report session creation, status and logout routes, the deprecated `GET /api/key-reports/reports`, server-sent events and WebSocket upgrades MUST NOT pass through a compressing wrapper. Content-hashed assets under `/assets/`, including fonts, MUST be served with immutable year-long `Cache-Control`; `index.html` MUST remain `no-cache`. Chart vendor code MUST NOT load before first paint: it MUST live in an async-only chunk that is neither statically imported by the entry chunk nor modulepreloaded, and the default `/dashboard` route MUST NOT load it.

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

#### Scenario: Owner key page reads are compressed

- **WHEN** a key owner's browser requests `GET /api/key-reports/page` with `Accept-Encoding: gzip` and the JSON body is at least 1 KiB
- **THEN** the response is gzip-encoded with `Vary: Accept-Encoding` and keeps `Cache-Control: no-store` and `Vary: Cookie`

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

### Requirement: Accounts and API trend chart x-axis uses MM-DD format

The x-axis tick format of the Account Trend chart and of the key page's daily charts SHALL be `MM-DD` (month and day extracted from the ISO date or timestamp data key), matching the reports chart convention. This format SHALL be locale-independent. The APIs-tab API Trend chart no longer exists; the key page replaces it.

#### Scenario: Account trend chart x-axis ticks

- **WHEN** the Account Trend chart renders with timestamp data
- **THEN** the x-axis tick labels SHALL be in `MM-DD` format (e.g., `"08-09"`)

#### Scenario: API trend chart x-axis ticks

- **WHEN** the key page renders its cost-per-day chart, which replaces the API Trend chart
- **THEN** the x-axis tick labels SHALL be in `MM-DD` format (e.g., `"08-09"`)

## REMOVED Requirements

### Requirement: APIs tab shows a 7-day account-cost donut for selected API keys

**Reason**: The APIs-tab detail pane is replaced by the key page `/keys/:id`, whose period picker is the only period control and whose cost-by-account table covers the selected period.

**Migration**: Open the key page (`GET /api/api-keys/{id}/page`); `GET /api/api-keys/{id}/usage-7d` keeps answering for one release.

### Requirement: APIs tab account-cost donut uses existing account labels and privacy rules

**Reason**: The account-cost donut is removed with the APIs-tab detail pane.

**Migration**: The key page's cost-by-account table uses account labels with the same privacy blur.

### Requirement: APIs tab account-cost donut follows the dashboard donut visual system

**Reason**: The account-cost donut is removed with the APIs-tab detail pane.

**Migration**: The key page's cost-by-account table replaces it.

### Requirement: APIs tab usage trend control layout is compact in the split view

**Reason**: The 7-day hourly trend chart is removed with the APIs-tab detail pane.

**Migration**: The key page shows cost per local day for the selected period; `GET /api/api-keys/{id}/trends` keeps answering for one release.
