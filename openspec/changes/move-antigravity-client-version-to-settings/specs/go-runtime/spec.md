## ADDED Requirements

### Requirement: The Antigravity client version is a runtime setting

The runtime settings SHALL hold the Antigravity client version used in the User-Agent of Cloud Code calls, defaulting to `2.34.0` and accepting only `<major>.<minor>.<patch>` versions. `GET /api/settings` SHALL return it as `antigravityClientVersion` and `PUT /api/settings` SHALL accept and validate it. Sign-in control calls and inference SHALL read the current value on each call, so a saved version applies without restart; an unreadable value SHALL refuse the call before dispatch. The runtime MUST NOT read `CODEX_LB_ANTIGRAVITY_CLIENT_VERSION`.

#### Scenario: Raising the version
- **WHEN** an administrator saves `2.35.0` as the Antigravity client version
- **THEN** the next Cloud Code call carries `antigravity/hub/2.35.0` in its User-Agent without a restart
