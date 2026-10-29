## MODIFIED Requirements

### Requirement: Account views display subscription time remaining

The Accounts list SHALL show two subscription countdowns beside each account when `subscriptionActiveUntil` is available: time remaining to the recorded end, followed by `/` and time remaining to that timestamp plus 14 × 24 hours. The second countdown SHALL be labeled as a +14-day reference, not a confirmed extension. Both countdowns SHALL remain visible in the account layout. The presentation SHALL include a tooltip with both exact dates, following the date-display preference, and explain that the date comes from saved authorization metadata and may lag renewal. Missing or invalid dates SHALL be visibly unknown, without inventing a second date. Each past date SHALL be labeled as elapsed independently rather than definitive loss of subscription access. Subscription labels SHALL be localized through the existing translation system. The derived date SHALL NOT alter stored subscription metadata, account eligibility, routing or quotas.

#### Scenario: Future subscription date

- **WHEN** an account has a future subscription end
- **THEN** the Accounts list shows compact days, hours, or minutes countdowns to the recorded end and that end plus 14 days
- **AND** both countdowns continue updating while the view remains open even if fetched account data is unchanged
- **AND** both exact dates are available in the tooltip

#### Scenario: Unknown subscription date

- **WHEN** subscription metadata is absent or invalid
- **THEN** the Accounts list shows that the subscription date is unknown without a derived countdown
- **AND** it does not substitute a quota reset time or token expiration

#### Scenario: Recorded period has ended

- **WHEN** the current time reaches the recorded subscription end
- **THEN** the first subscription label indicates that the recorded period ended
- **AND** the +14-day reference continues counting down until its own timestamp is reached, then indicates that it elapsed
- **AND** existing account status, controls, and quota displays retain their behavior

### Requirement: Dashboard limit warm-up controls

The dashboard SHALL expose global limit warm-up controls in Settings and per-account opt-in/status on the Accounts page. The global default SHALL be disabled. Settings SHALL include an exhausted-threshold percent control that determines which pre-reset usage samples count as exhausted for reset-confirmed warm-up.

#### Scenario: Configure warm-up behavior
- **WHEN** an operator opens Settings
- **THEN** the dashboard shows controls for enabling limit warm-up, selecting primary/secondary/both windows, setting the warm-up model, setting the prompt, setting the exhausted threshold, and setting the cooldown

#### Scenario: Validate warm-up settings before save
- **WHEN** an operator edits warm-up model, prompt, exhausted threshold, or cooldown fields
- **THEN** the dashboard enforces the same non-empty, max-length, percent, and integer cooldown bounds as the backend API before enabling save

#### Scenario: Show per-account opt-in and last attempt
- **WHEN** account summaries include limit warm-up status
- **THEN** the dashboard shows whether warm-up is enabled for that account
- **AND** it shows the latest attempt window, status, model, and completion/attempt time when available

#### Scenario: Warm-up controls are accessible by name
- **WHEN** an operator navigates the dashboard with assistive technology
- **THEN** global and per-account warm-up toggles expose descriptive accessible names that identify the setting and account context

### Requirement: Dashboard header shows the total available reset-credit count

The dashboard top navigation SHALL render a badge on the Accounts tab, pinned to the tab's upper-right radius, from the shell status that the current page's poll or `GET /api/dashboard/status` returns, without requesting `/api/accounts`. The badge SHALL show the number of accounts needing re-login when it is greater than 0; otherwise, when reset-credit badges are enabled, it SHALL show `availableResetCredits`, the sum of available reset credits in the in-memory snapshot. The badge SHALL display `99+` when the number exceeds 99 and SHALL be hidden when it is 0.

#### Scenario: Accounts tab shows the summed total
- **WHEN** the shell status reports no account needing re-login and 14 available reset credits
- **THEN** the Accounts nav tab displays a badge with `14`

#### Scenario: Accounts tab caps large totals
- **WHEN** the shell status reports 120 available reset credits
- **THEN** the Accounts nav tab displays a badge with `99+`

#### Scenario: Accounts tab hides empty totals
- **WHEN** the shell status reports no account needing re-login and no available reset credits
- **THEN** the Accounts nav tab displays no badge

#### Scenario: Re-login takes precedence
- **WHEN** two accounts need re-login
- **THEN** the Accounts nav tab displays a badge with `2` regardless of reset credits

### Requirement: App header brand links to dashboard

The app header brand area SHALL render a `<Link to="/overview">` wrapping the
logo and "Codex LB" text so that clicking the brand navigates back to the
Overview page. The link SHALL preserve the existing visual layout (logo
size, gradient background, text styling) and SHALL include keyboard
focus-visible ring styling matching the project's existing interactive-element
conventions.

#### Scenario: Brand click navigates to dashboard

- **WHEN** an operator clicks the header brand area (logo or "Codex LB" text)
- **THEN** the SPA navigates to `/overview`

#### Scenario: Brand link is keyboard-accessible

- **WHEN** an operator tabs to the header brand
- **THEN** the brand area receives a visible focus ring
- **AND** pressing Enter navigates to `/overview`

#### Scenario: Brand link preserves visual appearance

- **WHEN** the header renders
- **THEN** the logo and "Codex LB" text appear visually identical to the prior
  non-interactive `<div>` layout

### Requirement: Dashboard status separates service readiness from usage synchronization

The fixed dashboard status bar MUST render independent `Service ready`, `Quota synced` and `Usage folded` signals from the shell status, which the current page's poll response carries or which `GET /api/dashboard/status` returns every 60 seconds on pages without their own poll. `Service ready` MUST use the shell status `ready` flag, which reports the same readiness as `/health/ready`, and MUST treat a failed status request as not ready; the status bar MUST NOT request `/health/ready` itself. `Quota synced` MUST be derived only from `lastSyncAt` (the latest quota observation of non-deleted ChatGPT accounts), MUST show its relative and absolute time and MUST be fresh only while that timestamp is less than 60 seconds old. `Usage folded` MUST show the usage rollup fold time and whether the rollups are usable. The status bar MUST NOT request a page endpoint. The service-readiness signal MUST NOT use upstream account or provider health. The dashboard layout MUST reserve at least the status bar's rendered height so wrapped status rows do not cover page content.

#### Scenario: Ready service with stale usage

- **WHEN** the shell status reports `ready: true`
- **AND** `lastSyncAt` is absent or at least 60 seconds old
- **THEN** the status bar shows the service as ready
- **AND** independently shows quota sync as stale

#### Scenario: Unready service with fresh usage

- **WHEN** the shell status reports `ready: false` or cannot be fetched
- **AND** `lastSyncAt` is less than 60 seconds old
- **THEN** the status bar shows the service as not ready
- **AND** independently shows quota sync as synced

#### Scenario: Readiness is still being checked

- **WHEN** the initial shell status has not arrived
- **THEN** the service-readiness signal shows a checking state
- **AND** the quota synchronization signal shows no time yet

#### Scenario: Status signals wrap onto additional rows

- **WHEN** the fixed status bar grows because its signals wrap
- **THEN** the dashboard updates its reserved bottom space to the rendered
  status-bar height
- **AND** the fixed status bar does not cover page content

#### Scenario: Non-dashboard pages do not load the overview

- **WHEN** an operator stays on the Settings page
- **THEN** the status bar refreshes from `GET /api/dashboard/status`
- **AND** no `GET /api/overview` request is issued

### Requirement: Dashboard and report labels identify active conversations

The report conversation summary card MUST use the localized
equivalent of `Active Conversations`; its numeric value and ordering MUST
remain unchanged and it MUST NOT show an average of requests per conversation.

#### Scenario: Report uses the active-conversation label

- **WHEN** the report summary cards render
- **THEN** the conversation card label is `Active Conversations` in English
- **AND** its numeric value remains the existing distinct conversation total
- **AND** no `Avg req/conv` metadata is rendered on the report card

### Requirement: Dashboard conversation listing

The authenticated dashboard MUST expose `GET /api/conversations`. The list
endpoint MUST accept `limit`, `offset`, `search`, `since`, and `timeframe` query
parameters. The server-authoritative `timeframe` parameter MUST accept `1h`, `24h`,
`1d`, `7d`, or `30d` (`24h` and `1d` are the same window); when it is supplied, the server MUST derive the activity window
from the shared dashboard timeframe configuration and the client MUST NOT
substitute a browser-clock-generated `since` value. `timeframe` and `since` MUST
not be supplied together. When `since` is omitted, the server MUST apply a
rolling 30-day lower bound;
explicitly older `since` values MUST be capped at that same bound, and incoming
timezone-aware datetimes MUST be normalized to naive UTC before querying. It
MUST aggregate eligible `request_logs` rows by the raw, non-empty
`conversation_id` column, excluding rows whose request kind is `warmup` or
`limit_warmup`, and rows with `deleted_at IS NOT NULL`. Production request-log
writes MUST normalize ASCII padding and blank conversation IDs before storage;
conversation list, facet, and detail queries MUST use raw-column
`conversation_id` predicates and grouping rather than function-wrapped
expressions.

Search MUST be case-insensitive and match the normalized conversation ID or any
eligible row's user-agent family. Search MUST select whole conversations first:
after a conversation matches, aggregation MUST include all eligible rows in that
conversation, including rows whose user-agent family or ID did not match the
search text. The endpoint MUST derive aggregates from `request_logs` only.

When `since` is provided, a conversation MUST be selected when at least one
eligible row has `requested_at >= since`. A conversation MAY have eligible rows
before `since` and MUST still be included when it has activity in the window.
The grouped summary MUST aggregate all eligible rows for every selected
conversation, so `firstRequest`, `lastRequest`, `requestCount`, token totals,
cached-token totals, and cost MUST NOT be clipped to the window. Membership MUST
be implemented as an in-window aggregate condition and MUST NOT use a global
pre-window ID set or a pre-window anti-join.

After page membership is selected, the account, API-key, and model facet
queries for the returned page MUST use the same full eligible-row scope as the
summary, restricted only by the selected page's raw `conversation_id` values.
The facet queries MUST NOT add a `requested_at >= since` restriction after
membership selection; facet representatives and remaining counts MUST include
eligible history before `since` and MUST remain consistent with the full-history
summary aggregates.

The response MUST contain `conversations`, `total`, and `hasMore` pagination
fields. Each row in `conversations` MUST contain exactly these fields and no
response summary object:

- `conversationId`: the normalized, non-empty conversation identity.
- `firstRequest`: the earliest `requested_at` among all eligible rows in the
  conversation.
- `lastRequest`: the latest `requested_at` among all eligible rows in the
  conversation.
- `requestCount`: the number of eligible rows in the conversation.
- `representativeAccount` and `remainingAccountCount`.
- `apiKeyId` and `apiKeyName`.
- `representativeModel` and `remainingModelCount`.
- `totalTokens`.
- `cachedInputTokens`.
- `totalCostUsd`.

The camelCase names above are the external Dashboard API JSON contract. Python
schema, service, and repository identifiers MAY remain snake_case internally;
internal names MUST NOT be emitted as alternate response fields.

`totalTokens` MUST equal total input tokens plus total output tokens, with
`reasoning_tokens` used for a row when `output_tokens` is null.
`cachedInputTokens` MUST use the existing per-row clamp: null remains null;
otherwise the cached value is clamped to `[0, input_tokens]` when input tokens
are present. At aggregate level, null per-row values MUST NOT be converted to
zero; when every eligible row has a null cached value, `cachedInputTokens` MUST
be null, and otherwise it MUST equal the sum of the known clamped values.

Representative account values MUST use `request_count DESC,
latest_requested_at DESC, lexical account ASC`. List model values MUST be
grouped by distinct model, combining all reasoning efforts for that model, and
the representative model MUST use `request_count DESC, latest_requested_at DESC,
model lexical ASC`. Null account values MUST be excluded from account
candidates; if no non-null account exists, `representativeAccount` MUST be null
and `remainingAccountCount` MUST be 0. The list MUST NOT split model values by
`reasoning_effort`; `(model, reasoning_effort)` grouping MUST be used only for
conversation details.

Nullable and multiple-key conversations MUST be handled deterministically. Null
API-key values MUST not be candidates; if no non-null key exists, both API-key
fields MUST be null. When multiple distinct non-null keys exist, `apiKeyId` MUST be selected by
`request_count DESC, latest_requested_at DESC, lexical API-key ID ASC`, and
`apiKeyName` MUST be the corresponding existing dashboard-safe display name.
`apiKeyName` MUST never expose a secret, hash, or plaintext key material.

The list order MUST be stable: `lastRequest DESC`, then normalized
`conversationId ASC`. Pagination MUST be applied after this ordering.

#### Scenario: Pagination uses the stable list order

- **GIVEN** matching conversations have different latest request times and a
  tie exists on `lastRequest`
- **WHEN** the client calls `GET /api/conversations?limit=10&offset=20`
- **THEN** rows are ordered by `lastRequest DESC` and ties by normalized
  `conversationId ASC`
- **AND** the response starts at the 21st row in that order and reports the
  matching total and whether another page exists

#### Scenario: Blank IDs, warmups, and soft-deleted rows are excluded

- **GIVEN** request logs include null IDs, whitespace-only IDs, `warmup` rows,
  `limit_warmup` rows, soft-deleted rows, and eligible rows with non-empty IDs
- **WHEN** the client calls `GET /api/conversations`
- **THEN** only rows whose request kind is neither `warmup` nor `limit_warmup`,
  which are non-soft-deleted and have non-empty normalized IDs, contribute to
  returned conversations

#### Scenario: Search selects whole conversations

- **GIVEN** one eligible conversation contains a matching user-agent family on
  one row and non-matching user-agent/ID values on other rows
- **WHEN** the client calls `GET /api/conversations?search=opencode`
- **THEN** that conversation is selected
- **AND** all eligible rows in that conversation contribute to its counts,
  tokens, cached tokens, and cost
- **AND** rows from conversations with no matching ID or user-agent family are
  not returned

#### Scenario: List search is case-insensitive over normalized IDs and user-agent families

- **GIVEN** an eligible conversation has a normalized ID and user-agent family
  whose letters differ in case from the search text
- **WHEN** the client calls `GET /api/conversations?search=OPENCODE`
- **THEN** the conversation is selected when either the normalized ID or any
  eligible row's user-agent family matches case-insensitively

#### Scenario: Since filter selects conversations active in the window

- **GIVEN** conversation `conv-old` has its earliest eligible row at `t-10d`
  and a later row at `t-1d`, and conversation `conv-new` has its earliest
  eligible row at `t-1d`
- **WHEN** the client calls `GET /api/conversations?since=<t-7d ISO>`
- **THEN** both `conv-new` and `conv-old` are returned
- **AND** `conv-old` is included because it has a row inside the window even
  though its first message predates the window
- **AND** both conversations' summaries aggregate every eligible row, not only
  rows at or after `since`

#### Scenario: Since membership and facets share the full conversation scope

- **GIVEN** a selected conversation has eligible account, API-key, and model
  values both before and after the `since` boundary
- **WHEN** the client calls `GET /api/conversations?since=<ISO>`
- **THEN** `firstRequest`, `lastRequest`, `requestCount`, and summary totals
  include all eligible rows for the conversation
- **AND** account, API-key, and model facet counts and representatives include
  all eligible rows in the selected conversation, including rows before `since`

#### Scenario: Since filter composes with search and pagination

- **GIVEN** two conversations have activity inside the `since` window and only
  one matches the search text
- **WHEN** the client calls `GET /api/conversations?since=<ISO>&search=opencode`
- **THEN** only the matching conversation is returned
- **AND** the response total and hasMore reflect the since-and-search filtered
  set

#### Scenario: List model representatives ignore reasoning effort

- **GIVEN** a conversation has requests for the same model with multiple
  reasoning-effort values and requests for another model
- **WHEN** the client calls `GET /api/conversations`
- **THEN** the list groups the same model's requests into one model value
- **AND** the representative model is ordered by request count descending,
  latest request descending, and model lexical ascending
- **AND** the remaining model count counts distinct models, not model/effort
  combinations

#### Scenario: API-key representation is safe and deterministic

- **GIVEN** a conversation has null API-key rows and multiple non-null API-key
  values with tied counts
- **WHEN** the client calls `GET /api/conversations`
- **THEN** null values do not become the representative
- **AND** the non-null representative is selected by count, latest request, and
  lexical API-key ID
- **AND** the response contains only the corresponding dashboard-safe name and
  never secret, hash, or plaintext key material

### Requirement: Dashboard conversation view

The Requests page (`/requests`) MUST render Request Logs by default and MUST offer a view switch between Request Logs and Conversations. Selecting Conversations MUST persist `view=conversations` in the URL; selecting Request Logs MUST return to the request-log view. The list section title MUST be the single selector trigger for Request Logs and Conversations, keeping the original uppercase typography. Both views MUST share one period selector with exactly the options `1h`, `24h`, `7d` and `30d`, defaulting to `24h`, persisted in the URL as `timeframe` (absent for the default) and sent to the list endpoints as the symbolic `timeframe` parameter, never as a browser-clock-derived `since`; changing it MUST reset pagination to the first page. There MUST be no unbounded "all" option. The page MUST NOT poll unless its Live toggle is on.

The page MUST retain separate URL-backed filters and pagination for Request Logs and Conversations. Switching views MUST NOT reinterpret, overwrite, or clear the inactive view's query state.

The Conversations view MUST NOT render a free-text filter input above the list and MUST use the list endpoint's established loading, error, empty, and pagination behavior.

The conversation list MUST render exactly these columns in order: Last request, Conversation, Accounts, API key, Models, Tokens, Cost, and Details. Last request MUST use the request-log Time column's two-line time/date presentation. Accounts MUST resolve the representative account ID through the directory accounts and display the alias, then email, then the ID as a final fallback. Accounts and models MUST render remaining values as a smaller muted `+ N more` secondary line. Tokens MUST show total tokens with cached input tokens on a subordinate line. When dashboard privacy blur is enabled, an account label resolved from an email fallback MUST render with the established `privacy-blur` class; alias and account-ID fallback labels MUST remain unblurred. The API-key column MUST use `apiKeyName` only. Details MUST use the existing Details button treatment.

The details dialog MUST render row one as conversation ID, start, and latest; row two as account count, total elapsed time, and dominant user-agent family; and a model/effort table with exactly these displayed columns, in order: Model (effort), Reqs, Total elapsed, Total input (with total cache as a subordinate/parenthetical value), Total output, and Total cost. The table MUST default to Reqs descending and MUST support client-side sorting for every displayed column. The displayed conversation ID MUST NOT provide a copy action. The dialog MUST use the established loading state while the detail API is pending and the standard error display with retry for unknown or malformed conversation IDs; nullable optional aggregate values MUST render the established em-dash fallback. An empty conversation list MUST render the established empty state; for a nonzero offset the view MUST retain its pagination controls, and at offset zero it MUST NOT render them.

#### Scenario: Request Logs is the default and selector switches views

- **WHEN** an operator opens `/requests`
- **THEN** Request Logs of the last 24 hours is visible and active by default
- **WHEN** the operator selects Conversations through the title selector
- **THEN** the Conversations list renders and the URL contains `view=conversations`

#### Scenario: Request Logs and Conversations retain independent URL query state

- **GIVEN** Request Logs has active filters and pagination and Conversations has different pagination retained in the URL
- **WHEN** the operator switches between the two views
- **THEN** each view restores its own filters and pagination
- **AND** switching views does not reinterpret, overwrite, or clear the other view's query state

#### Scenario: Conversations has no free-text filter and renders the day selector

- **WHEN** the operator opens the Conversations view
- **THEN** no free-text filter input is rendered above the list
- **AND** the shared period selector offers exactly `1h`, `24h`, `7d` and `30d`, defaults to `24h` and offers no unbounded "all" option
- **AND** the list renders the specified columns and two-line request time presentation

#### Scenario: Conversation day selector persists in the URL and drives timeframe

- **WHEN** the operator changes the period selector from `24h` to `30d`
- **THEN** the URL gains `timeframe=30d` (or drops the parameter when the default `24h` is selected)
- **AND** the list endpoint is called with `timeframe=30d`
- **AND** the list endpoint does not receive a browser-clock-derived `since`
- **AND** pagination resets to the first page

#### Scenario: Conversation timeframe drives active dashboard statistics

- **GIVEN** the URL restores `timeframe=7d`
- **WHEN** the operator opens the Conversations view
- **THEN** the conversation list and the request log both use the `7d` window
- **AND** no overview or statistics request is issued by the Requests page

#### Scenario: Conversation day selector state is independent per view

- **GIVEN** the period selector is shared by both views
- **WHEN** the operator switches to Request Logs and back to Conversations
- **THEN** the selected period is unchanged
- **AND** each view keeps its own pagination

#### Scenario: Conversation account privacy blur applies only to email fallback

- **GIVEN** dashboard privacy blur is enabled
- **WHEN** the Conversations list renders account labels from alias, email fallback, and account-ID fallback values
- **THEN** only the email-fallback label has the established `privacy-blur` class
- **AND** the alias and account-ID fallback labels remain unblurred

#### Scenario: The original-styled title is the only view selector

- **WHEN** the list section renders
- **THEN** its uppercase title typography is retained
- **AND** activating the title opens the Request Logs/Conversations selector
- **AND** no separate selector is rendered to the title's right

#### Scenario: Conversation details use established loading and retry states

- **WHEN** the detail API is loading for a selected conversation
- **THEN** the dialog uses the established loading state
- **WHEN** the detail API returns an unknown or malformed-ID error
- **THEN** the dialog uses the standard error display with retry

#### Scenario: Nullable detail aggregates use dashboard fallbacks

- **GIVEN** a successful detail response contains nullable optional aggregate values
- **WHEN** the operator opens the details dialog
- **THEN** each nullable value renders the established em-dash fallback without breaking the row or dialog

#### Scenario: Empty conversation results use the existing empty state

- **GIVEN** the conversation list response contains no rows
- **WHEN** the operator opens the Conversations view
- **THEN** the existing empty state is rendered

#### Scenario: Empty later conversation pages retain pagination controls

- **GIVEN** the operator is on a nonzero Conversations page and the response contains no rows
- **WHEN** the view renders the response
- **THEN** the empty state is rendered
- **AND** pagination controls remain visible

#### Scenario: Details dialog has the approved layout and sorting

- **WHEN** the operator opens a conversation's Details dialog
- **THEN** row one contains conversation ID/start/latest and the conversation ID has no copy action
- **AND** row two contains account count/total elapsed/dominant user-agent
- **AND** the table displays exactly Model (effort), Reqs, Total elapsed, Total input (with total cache as a subordinate/parenthetical value), Total output, and Total cost
- **AND** the table initially sorts by Reqs descending and every displayed column header reorders only the returned rows client-side

## REMOVED Requirements

### Requirement: Dashboard weekly credits pace

**Reason**: The dashboard page is replaced by the Overview page, whose pool strip and group rows show the weekly remaining and projected exhaustion once.

**Migration**: the Overview page (`GET /api/overview`) shows pool and group health, alerts and keys needing attention once, with fixed period labels; `GET /api/dashboard/projections` keeps its weekly pace for one release.

### Requirement: Dashboard projections load after the primary dashboard data

**Reason**: The Overview page has one poll and no chained projections request.

**Migration**: Projections are part of `GET /api/overview`; `GET /api/dashboard/projections` is deprecated.

### Requirement: Dashboard usage donuts present credits as stacked remaining and capacity

**Reason**: The usage donuts repeated the quota numbers the pool strip and group rows now show once.

**Migration**: the Overview page (`GET /api/overview`) shows pool and group health, alerts and keys needing attention once, with fixed period labels.

### Requirement: Dashboard account summaries sorted by primary capacity

**Reason**: The dashboard no longer lists accounts; account management lives on the Accounts page.

**Migration**: Use the Accounts page; the overview shows only problem accounts inside group rows.

### Requirement: Account card row height is 11.5rem

**Reason**: The dashboard account cards are removed.

**Migration**: Use the Accounts page.

### Requirement: Weekly credits pace header uses flex-start alignment

**Reason**: The weekly credits pace card is removed from the dashboard.

**Migration**: the Overview page (`GET /api/overview`) shows pool and group health, alerts and keys needing attention once, with fixed period labels.

### Requirement: Dashboard account cards show live credit state

**Reason**: The dashboard account cards are removed.

**Migration**: Purchased credits remain on the Accounts page; account health in group rows accounts for usable credits.

### Requirement: Dashboard accounts section shows account availability summary

**Reason**: The dashboard accounts section is removed.

**Migration**: The pool strip shows account health counts by state.

### Requirement: Dashboard overview summary cards show previous-window usage deltas

**Reason**: Period comparisons belong to Reports; the overview has no period picker.

**Migration**: Use the Reports page for period comparisons.

### Requirement: Dashboard estimated cost card meta avoids duplicate estimate and cache copy

**Reason**: The summary cards are removed with the dashboard page.

**Migration**: Spend today and over 7 days is shown in the pool strip and group rows.

### Requirement: Dashboard accounts section supports card and list views

**Reason**: The dashboard accounts section is removed.

**Migration**: Use the Accounts page.

### Requirement: Dashboard accounts section exposes a reset-credits redeem action

**Reason**: The dashboard accounts section is removed.

**Migration**: Redeem reset credits from the Accounts page.

### Requirement: Dashboard conversation card shows the filtered average

**Reason**: The summary cards are removed with the dashboard page.

**Migration**: Conversations are listed on the Requests page.

### Requirement: Dashboard overview and request-log listing fail independently

**Reason**: The overview and the request log are separate pages with their own loading and error states.

**Migration**: The Requests page keeps its previous rows with a retryable error; the Overview keeps its previous data with a stale label.
