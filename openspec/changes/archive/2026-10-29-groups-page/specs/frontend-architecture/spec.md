## MODIFIED Requirements

### Requirement: Settings page

The Settings page SHALL include sections for: routing settings (sticky threads,
reset priority, prompt-cache affinity TTL, weekly pace controls, limit warm-up
controls, and Fast Mode prohibition), password management
(setup/change/remove), TOTP management (setup/disable), API-key access (the API
key authentication toggle and the upstream-quota privacy toggle), and
sticky-session administration. API key management lives on the Keys and key
pages and account-group management on the Groups page; the Settings page SHALL
NOT render the API key table or the account-group list. The Appearance section
SHALL offer theme, time format, date format and account rows and SHALL NOT
offer a dashboard refresh rate or an account burn-projection toggle. API key
create and edit dialogs that expose reasoning effort choices MUST include
upstream-supported extended efforts such as `max` and `ultra`.

Advanced sections — routing settings, model sources, firewall, and
sticky-session administration — SHALL render inside an Advanced settings group
that is collapsed by default. Expanding the group SHALL take exactly one
interaction, after which every previously mandated section SHALL be reachable
and fully functional. While the group is collapsed, its sections SHALL NOT
mount, and the sections that self-fetch on mount — model sources, firewall, and
sticky-session administration — SHALL NOT issue their data requests; those
requests fire when the group is expanded. Core sections (appearance, import,
password management, session, TOTP, and API-key access) SHALL remain visible
without expanding the group. The Settings page SHALL NOT render upstream proxy
administration, quota phase planner or guest-access controls, which the Go
runtime does not provide.

#### Scenario: Advanced settings collapsed by default

- **WHEN** a user opens the Settings page
- **THEN** appearance, import, and API-key access sections are visible
- **AND** neither the API key table nor the account-group list is rendered
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

### Requirement: API keys settings expose quota privacy toggle
The Settings page SHALL include a toggle in the API-key access section that controls `hide_upstream_quota_from_api_keys`.

#### Scenario: Toggle is visible with the API keys controls

- **WHEN** the Settings page renders the API-key access section
- **THEN** the quota privacy toggle SHALL be shown alongside the API key auth toggle

#### Scenario: Toggle persists through settings save

- **WHEN** the user changes the quota privacy toggle
- **THEN** the settings update request SHALL include `hideUpstreamQuotaFromApiKeys`

### Requirement: Settings page renders in the active locale

The Settings page and its constituent sections (`Appearance`, `Routing`, `Import`, `Session`, `Password`, `TOTP`, `API-key access`) SHALL render all user-visible strings — page title and subtitle, section headings and descriptions, switch and select labels, button labels, dialog titles and descriptions, validation messages, warning banners, and toast messages — through the active i18n locale. Selecting `zh-CN` MUST translate the entire surface above to Simplified Chinese, with the explicit exceptions of the embedded `FirewallSection`, `QuotaPlannerSection`, `StickySessionsSection`, and `UpstreamProxySettings` rendered inside Settings, which belong to other capabilities and remain English until their own migrations.

#### Scenario: Switching to Simplified Chinese translates the Settings page

- **WHEN** a user opens the Settings page with `zh-CN` selected as the active language
- **THEN** the page heading reads `设置`
- **AND** every section heading inside the page (`外观`, `路由`, `导入`, `会话`, `密码`, `TOTP`) renders in Simplified Chinese
- **AND** every dialog opened from the page (`Set password`, `Change password`, `Remove password`, `Verify password`, `Enable TOTP`, `Disable TOTP`) renders its title, description, field labels, and submit button in Simplified Chinese

#### Scenario: Session lifetime invalid-input warning preserves the inline example

- **WHEN** a user enters a non-integer hour value such as `1.5` into the dashboard session lifetime input
- **THEN** the inline warning renders with `1.5` wrapped in `<code>` regardless of the active locale
- **AND** the surrounding sentence is translated according to the active locale

#### Scenario: TOTP code-length validation respects the active locale

- **WHEN** a user submits the Enable TOTP or Disable TOTP form with a code shorter than 6 characters and the active locale is `zh-CN`
- **THEN** the form-level validation message renders as `请输入 6 位验证码`
- **AND** the same submission with `en` active renders as `Enter a 6-digit code`
