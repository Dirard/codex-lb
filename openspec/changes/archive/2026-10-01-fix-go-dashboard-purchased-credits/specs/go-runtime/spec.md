## MODIFIED Requirements

### Requirement: Deliver selected functionality before cutover

The Go implementation SHALL provide the selected account/OAuth, many-to-many group, API-key, per-key limit, model, quota, price, report, admin-security, firewall, Codex Responses/file/image/voice, warmup/scheduled-ping and reset-credit functionality before production cutover. It SHALL NOT provide upstream install telemetry, multi-replica runtime coordination, the quota phase planner, guest dashboard access, PostgreSQL runtime, proxy pools, or a separate public Images API. Binary/systemd and optional ordinary Docker delivery SHALL replace Helm/Kubernetes, Nix and distroless-specific delivery. Local diagnostics and usage statistics MUST remain available independently of removed telemetry.

#### Scenario: Images used by Codex are retained
- **WHEN** Codex submits supported image input or uses its built-in image generation workflow
- **THEN** the proxy preserves that capability on a capable provider
- **AND** this does not require exposing `/v1/images/generations` or `/v1/images/edits`

#### Scenario: Account groups survive removal of proxy pools
- **WHEN** one account belongs to multiple groups
- **THEN** every group retains its independent membership, key policies and limits
- **AND** shared membership does not create additional upstream quota

#### Scenario: Purchased credits and separately metered quotas survive restart
- **WHEN** usage metadata is persisted or imported and the Go server restarts
- **THEN** purchased-credit status, monthly windows and additional quota windows retain their observed values and timestamps in routing and the dashboard
- **AND** usable purchased credits remain distinct from calculated subscription credits and permit admission after included primary, weekly or monthly windows are exhausted
- **AND** a model's canonical additional quota uses its own fresh windows and registry restrictions for new admissions, without falling back to standard windows when required evidence is absent
- **AND** the established-owner continuation exception remains unchanged
- **AND** absent additional-quota metadata preserves prior samples, an explicit empty list clears them, and older samples cannot replace newer values

## ADDED Requirements

### Requirement: Dashboard account credits reflect durable capacity and upstream refusal

Authenticated dashboard account summaries SHALL expose the observed applicable window metadata, known calculated subscription capacity and remaining credits, and persisted purchased-credit has/unlimited/balance fields separately. Unknown values MUST remain null, known exhausted balances MUST remain zero, and unlimited credits MUST retain their explicit flag. Purchased credits MUST NOT increase calculated subscription capacity. Account summaries and new-request selection SHALL use consistent effective quota status. Usable purchased credits SHALL permit exhausted included windows in both selection and transactional reservation; an explicit finite nonpositive balance SHALL override a generic has-credits flag unless unlimited is true. A stored upstream quota refusal MUST NOT be cleared for new admissions by a credit observation at or before that refusal. Newer usable credit evidence MAY restore credit-backed admission. Administrator-disabled states, actual rate-limit blocks, authentication, key/group/model restrictions and existing continuation ownership MUST remain enforced.

#### Scenario: Known, unknown and unlimited balances
- **WHEN** the overview contains accounts with observed subscription windows and purchased-credit metadata
- **THEN** account cards receive the same independent balance values as the account list, including zero and unlimited
- **AND** unknown plans or absent credit metadata are not represented as an invented balance

#### Scenario: Included quota exhausted but purchased credits remain
- **WHEN** a permitted account has exhausted its included primary or long window and has usable purchased credits without a newer upstream refusal
- **THEN** selection and reservation admit the request without marking the account exhausted solely because of included quota
- **AND** later explicit zero-credit metadata removes that override for new requests

#### Scenario: Confirmed refusal after a credit snapshot
- **WHEN** OpenAI refuses quota after the stored positive-credit observation
- **THEN** subsequent new requests and reservations cannot use that old balance to reopen the account
- **AND** normal failure settlement and eligible-account failover remain unchanged
