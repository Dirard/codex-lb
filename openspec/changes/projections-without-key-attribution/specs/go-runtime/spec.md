## MODIFIED Requirements

### Requirement: Read-only weekly-credit pace follows persisted settings
Dashboard overview/projections SHALL compute the selected read-only weekly-credit pace from fresh persisted quota observations, including saved UTC working weekdays and smoothing interval. Persisted observations are the newest observation of each five-minute bucket of an account window, with reset or window changes kept as separate observations. Missing samples SHALL remain distinguishable from zero burn, reset/window changes SHALL not be smoothed together, and stale/ineligible observations SHALL not inflate usable capacity. The report SHALL preserve its existing nullable forecast, confidence and reset-event contracts without controlling routing or initiating provider work. The deprecated dashboard overview SHALL keep its key attribution of the last two hours; the projections SHALL return an empty key attribution and SHALL NOT read usage events for it. Settings SHALL survive restart and import and affect the actual report, not merely be accepted by the settings API.

#### Scenario: Saved weekday and smoothing settings reach the report
- **WHEN** the administrator saves working weekdays or a supported smoothing interval and reloads the runtime
- **THEN** overview and projections SHALL apply those saved values to the same persisted observations without fetching provider data

#### Scenario: There is no fresh usable weekly observation
- **WHEN** all candidate observations are stale, invalid or operator-ineligible
- **THEN** weeklyCreditPace SHALL be null rather than showing fictional available credits or zero consumption

#### Scenario: An idle polled account
- **WHEN** an eligible account is polled without consumption for six hours
- **THEN** its history still contains an observation in every five-minute bucket so the pace reports zero burn rather than missing samples

#### Scenario: Projections without key attribution
- **WHEN** API keys made requests in the last two hours and the pool has a weekly credit pace
- **THEN** the overview's pace SHALL list the top keys of those hours, and the projections' pace SHALL carry the same forecast with an empty key attribution
