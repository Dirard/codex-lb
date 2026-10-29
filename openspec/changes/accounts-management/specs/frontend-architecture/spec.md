## MODIFIED Requirements

### Requirement: Accounts page

The Accounts page SHALL display every account in one table (`AccountsTable`) under a toolbar with search, status and group filters, sort and Add account, and SHALL open the selected account's details, including usage, token info, groups, settings and actions (pause/resume/delete/re-authenticate), in a detail sheet (`AccountDetailSheet`). The Accounts page SHALL also let operators view and update whether an account is authorized for upstream cybersecurity work without losing existing account actions such as pause, resume, re-authenticate, export, and delete.

The layout SHALL fit mobile, tablet, and desktop dashboard widths without horizontal page overflow: the table SHALL scroll horizontally inside its own container and the detail sheet SHALL fit the viewport width.

The Accounts page SHALL keep the toolbar, including Add account, outside the table rows so it remains reachable without scrolling through existing accounts.

Account status displays and filters SHALL distinguish `reauth_required` accounts from `deactivated` accounts: `reauth_required` means the local credential/session must be refreshed by operator re-authentication, while `deactivated` means the upstream account is disabled, suspended, deleted, or explicitly deactivated.

#### Scenario: Account security-work authorization is toggled

- **WHEN** an operator toggles Trusted Access for Cyber for an account
- **THEN** the app sends the account update request with the requested `securityWorkAuthorized` value
- **AND** the account list and dashboard overview data are invalidated after the update succeeds

#### Scenario: Security-work authorization appears in account summaries

- **WHEN** an account summary has `securityWorkAuthorized=true`
- **THEN** the Accounts page shows that account as eligible for Trusted Access for Cyber routing

#### Scenario: Same-email workspace slots are distinguishable

- **WHEN** the account list contains multiple accounts with the same email
- **AND** at least one account has workspace metadata
- **THEN** the table rows and the detail sheet show workspace identity or compact account id context sufficient to distinguish the credential slots

#### Scenario: Same-login workspace slots are preserved

- **WHEN** multiple imported or OAuth-completed credentials share the same ChatGPT account identity
- **AND** they carry distinct workspace ids or workspace labels
- **THEN** each workspace credential is preserved as a separate local account slot

#### Scenario: Import copy reflects credential slots

- **WHEN** a user views import settings
- **THEN** the copy describes preserving separate workspace or unknown credential slots instead of email-level duplicates

#### Scenario: Responsive account management layout

- **WHEN** the Accounts page is rendered at a mobile-width viewport
- **THEN** the toolbar controls wrap within the viewport, the table scrolls horizontally inside its container and the detail sheet spans the viewport width
- **AND** quota rows, routing policy controls, token status, and action buttons fit within the viewport without horizontal document overflow

#### Scenario: Add account remains outside account list scrolling

- **WHEN** the Accounts page renders the toolbar
- **THEN** the Add account button is not a child of the table
- **AND** the button remains available without scrolling through existing accounts

#### Scenario: Long account list scrolls inside the left panel

- **WHEN** the Accounts page renders more account rows than fit in the viewport
- **THEN** every row stays reachable by scrolling, the table scrolls horizontally only inside its own container
- **AND** the toolbar with Add account is rendered above the rows, outside the table

#### Scenario: Re-authentication-required account is labeled separately

- **WHEN** an account summary has `status = "reauth_required"`
- **THEN** the table row and the detail sheet status badge show `Re-auth required`
- **AND** the account can be found with the status filter for `reauth_required`
- **AND** the row and the detail sheet expose the re-authenticate action
- **AND** neither exposes pause or resume actions that could bypass re-authentication
- **AND** neither exposes routing-policy controls that imply the account is selectable while operator recovery is required

### Requirement: Accounts list surfaces quota reset timing

The accounts table SHALL render a 5h column and a weekly column, and a monthly column when any listed account has a monthly window; each cell SHALL show the remaining percentage and the time remaining until reset when a reset timestamp is available, and an absent window SHALL render as `—`. Each row SHALL also surface whether the account is opted in to limit warm-up.

#### Scenario: Regular account shows both quota rows
- **WHEN** the table renders an account with both primary and weekly quota windows
- **THEN** the row shows both the 5h and the weekly remaining
- **AND** each shows its reset countdown

#### Scenario: Weekly-only account omits the 5h row
- **WHEN** the table renders an account whose primary window is absent
- **THEN** the row's 5h cell shows `—`
- **AND** the weekly cell still shows the remaining and the reset

#### Scenario: Limit warm-up opt-in is visible
- **WHEN** the table renders an account with limit warm-up opt-in enabled
- **THEN** the row shows the opt-in state

### Requirement: Accounts list orders by next reset
The accounts table's "Next reset" sort SHALL order accounts by the earliest upcoming quota reset timestamp among their 5h, weekly and monthly windows. Accounts without any upcoming reset timestamp SHALL sort after accounts with one. When reset timestamps are equal or unavailable, the table SHALL fall back to a stable order by label and account id.

#### Scenario: Earlier reset sorts first
- **WHEN** the operator sorts by next reset and one account has an earlier quota reset time than the other
- **THEN** the earlier-reset account appears before the later-reset account

### Requirement: Accounts list supports explicit sort modes

The accounts table SHALL expose sort modes for status severity, reset time soonest-first, reset time latest-first, 5h remaining (lowest first), weekly remaining (lowest first), most reset credits, account name ascending, and account name descending. The default sort mode SHALL be status severity, which orders re-login required, deactivated, quota exceeded, rate limited, paused and then active accounts, each status by reset time soonest-first. Accounts without a value for the selected measure SHALL sort after accounts with one. The selected sort mode SHALL apply only to the rendered rows; it SHALL NOT change which account the detail sheet shows.

#### Scenario: Reset soonest remains the default

- **WHEN** the table renders without an explicit sort mode
- **THEN** accounts needing re-login come first and active accounts last
- **AND** accounts of the same status with the earliest upcoming quota reset sort first

#### Scenario: Reset latest sorts finite resets descending

- **WHEN** a user selects reset time latest-first
- **THEN** accounts with later upcoming quota resets sort before
  accounts with earlier upcoming quota resets
- **AND** accounts without an upcoming reset timestamp sort after
  accounts with finite upcoming reset timestamps

#### Scenario: Remaining sorts lowest first

- **WHEN** a user selects 5h remaining or weekly remaining
- **THEN** accounts with less remaining in that window sort first
- **AND** accounts without that window sort last

#### Scenario: Name sort modes order by account label

- **WHEN** a user selects account name ascending or descending
- **THEN** the table orders accounts by alias, display name, email, or account
  identifier in the selected direction

### Requirement: Accounts list uses available tall-viewport space

The Accounts page MUST let the accounts table grow with its rows instead of bounding it to a fixed internal height, so a long account list uses the whole viewport and scrolls with the page. The toolbar with search, filters, sort, help and Add account MUST stay above the rows. The account rows MUST remain reachable above the fixed status bar.

#### Scenario: Tall desktop viewport expands the rows region

- **WHEN** the Accounts page renders a long account list in a 1200px-tall desktop viewport
- **THEN** the table is not limited to a fixed height smaller than the viewport
- **AND** the final row can be scrolled above the fixed status bar

#### Scenario: Expanded help panel consumes rows space

- **WHEN** a user expands Windows OAuth Help above a long account list
- **THEN** the help panel renders between the toolbar and the table, outside the rows
- **AND** the final account remains reachable by scrolling the page

#### Scenario: Shorter account list does not stretch its card

- **WHEN** all account rows fit within the viewport
- **THEN** the table ends after the final row and its normal bottom padding
- **AND** it does not render an empty bordered area beneath the final account

#### Scenario: Account pool still exceeds the available height

- **WHEN** the account rows require more space than the viewport provides
- **THEN** the rows remain reachable through the final account by scrolling the page
- **AND** the Add account action remains in the toolbar above the rows

### Requirement: Account list presents a single add-account entry point with a chooser dialog

The accounts toolbar SHALL present account creation through a single "Add account" control instead of separate always-visible "Import" and "Add Account" buttons. Activating it SHALL open a modal chooser dialog offering two options: adding an account via OAuth and importing an exported `auth.json` file. Selecting an option SHALL close the chooser and open the corresponding existing flow (the OAuth dialog or the import dialog) via the existing handlers, without changing those flows' behavior. When there are no accounts, the empty table SHALL offer the same control.

#### Scenario: Add-account placeholder opens the chooser

- **WHEN** an operator views the Accounts page
- **THEN** a single "Add account" control is shown in the toolbar
- **AND** no separate always-visible "Import" or "Add Account" buttons are present
- **WHEN** the operator activates the control
- **THEN** a chooser dialog opens offering an "Add account" (OAuth) option and an "Import" option

#### Scenario: Choosing an option opens its existing flow

- **GIVEN** the add-account chooser dialog is open
- **WHEN** the operator selects the "Add account" option
- **THEN** the chooser closes and the existing OAuth sign-in dialog opens
- **WHEN** the operator instead selects the "Import" option
- **THEN** the chooser closes and the existing `auth.json` import dialog opens

### Requirement: Accounts page can sort by available reset credits

The accounts table sort selector SHALL offer a "Most reset credits" option. That ordering SHALL sort accounts by `available_reset_credits` descending. Ties SHALL be broken by `reset_credit_nearest_expires_at` ascending (soonest expiring first), and accounts with no expiry SHALL sort after accounts that have one.

#### Scenario: More available credits sorts first
- **WHEN** the operator selects "Most reset credits"
- **AND** account A has `available_reset_credits: 4` and account B has `available_reset_credits: 1`
- **THEN** account A appears before account B

#### Scenario: Tie breaks by soonest expiry
- **WHEN** two accounts have equal `available_reset_credits`
- **AND** one account's soonest credit expires before the other's
- **THEN** the account with the earlier `reset_credit_nearest_expires_at` appears first

## REMOVED Requirements

### Requirement: Accounts list respects compact row appearance preference

**Reason**: The accounts table shows the 5h and weekly windows in their own columns for every account, so there is no compact row to choose.

**Migration**: Hide a column by sorting or filtering instead; the detail sheet shows every window.

### Requirement: AccountListItem displays a reset-credits count badge

**Reason**: `AccountListItem` is replaced by the accounts table, whose reset-credits column shows the snapshot count of every account.

**Migration**: Read the reset-credits column, or sort by "Most reset credits".

### Requirement: Account list status filter shares the help row

**Reason**: The list controls are replaced by the accounts toolbar, which holds search, the status and group filters, sort, the help toggle and Add account in one row that wraps on narrow screens.

**Migration**: The status filter is in the toolbar next to the search input.
