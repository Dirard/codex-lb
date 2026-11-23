## ADDED Requirements

### Requirement: Codex control aliases preserve canonical behavior

The Go runtime SHALL accept GET and POST at `/backend-api/codex/thread/goal/get`. Retained Codex control routes and Realtime call creation SHALL also accept their single trailing-slash equivalent with the same authentication, request limits and owner policy. Dispatch SHALL use the canonical upstream path without changing the incoming method, body or query. Unsupported methods MUST remain rejected and aliases MUST NOT bypass authorization or expose a usable Realtime call before owner binding.

#### Scenario: Codex reads a goal using POST
- **WHEN** an authorized client sends a POST body to the goal-read route or its trailing-slash equivalent
- **THEN** the provider receives that same method/body/query on the canonical goal-read path

#### Scenario: A client uses a trailing slash
- **WHEN** a client calls a retained control or Realtime creation route with one trailing slash
- **THEN** the canonical handler's authorization and behavior apply without an unnecessary redirect
