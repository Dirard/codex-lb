# Default probes and weekly-only quotas

- Last edited with skill pack: `0.2.2`

## ADDED Requirements

### Requirement: Default administrative probes use Luna without model fallback

An administrative probe or warmup without an explicit model SHALL use `gpt-6-luna`. Explicit configured/request models SHALL remain unchanged. Failure MUST NOT silently select another model or repeat the generation. Existing account pinning, administrator authentication and accounting SHALL remain enforced; unknown usage SHALL remain pending rather than be fabricated as zero.

#### Scenario: Probe without a model
- **WHEN** an authenticated administrator probes an eligible account without supplying a model
- **THEN** one request is sent to that account using `gpt-6-luna`
- **AND** a failure does not dispatch a more expensive fallback

### Requirement: A sole weekly quota is not a five-hour quota

A ChatGPT usage response with only a primary window lasting exactly seven days SHALL be represented as a weekly/secondary quota, with no five-hour/primary quota. Existing dual-window, unknown-duration and monthly-only handling SHALL remain compatible. A fresh complete nonempty standard-quota observation SHALL atomically replace older current standard windows; absent, incomplete or stale observations MUST NOT erase previously known windows. Account/credential/generation and pending plan-confirmation fences SHALL still apply. Replacing an incorrectly labeled seven-day primary window SHALL correct that account's corresponding seven-day history labels without changing observation values or financial usage. Weekly-only account views and legends SHALL not describe their quota as five-hour quota. No synthetic zero-percent primary quota SHALL be created.

#### Scenario: Weekly-only observation replaces the old mislabeled window
- **WHEN** a fresh provider observation contains one seven-day window at 41 percent used
- **THEN** accounts and dashboard show 59 percent weekly remaining and no primary quota
- **AND** the old primary row and its incorrect history labels do not continue to appear as five-hour capacity

#### Scenario: A partial or stale observation arrives
- **WHEN** usage windows are absent/incomplete or the observation predates accepted account state
- **THEN** the observation does not erase more recent known windows or bypass account identity fences

#### Scenario: Both standard windows exist
- **WHEN** the provider reports both five-hour and weekly windows
- **THEN** both periods retain their own usage, reset times and current display
