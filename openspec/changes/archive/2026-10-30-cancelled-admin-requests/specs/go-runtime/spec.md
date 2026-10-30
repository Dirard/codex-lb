## ADDED Requirements

### Requirement: Cancelled dashboard requests are not server errors

When a dashboard (admin API) request fails because its client cancelled it, so that the operation returns the request context's cancellation, the runtime SHALL answer 499 with code `request_cancelled` and SHALL NOT log it as a failed admin operation. Other unexpected admin failures SHALL keep 500 `internal_error` and their error log line.

#### Scenario: Browser drops a dashboard request
- **WHEN** an authenticated `/api/accounts` read fails with the request context's cancellation
- **THEN** the response is 499 `request_cancelled` and no `admin operation failed` line is logged

#### Scenario: Unexpected admin failure
- **WHEN** an authenticated `/api/accounts` read fails with any other error
- **THEN** the response is 500 `internal_error` and one `admin operation failed` line is logged
