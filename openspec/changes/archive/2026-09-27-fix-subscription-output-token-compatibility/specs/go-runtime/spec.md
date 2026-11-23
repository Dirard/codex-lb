## ADDED Requirements

### Requirement: Subscription requests omit unsupported output token caps

The ChatGPT subscription wire payload for Responses HTTP/WebSocket and compact SHALL omit `max_output_tokens`, including administrative probes and scheduled warmups. The caller's original request SHALL remain unchanged. External-provider Responses SHALL preserve the field and Chat Completions SHALL retain its existing translation. Removing the field SHALL NOT trigger another generation or change model/account ownership. The subscription backend's output MUST NOT be described as capped to that requested value.

For subscription Responses, the output-token estimate used for per-key reservation SHALL NOT be reduced below the normal uncapped estimate of 2048 tokens by the unsupported field; a larger supplied estimate SHALL remain conservative. The existing reservation clamp to remaining key budget and external-provider estimates SHALL retain their current behavior. Settlement SHALL use actual provider usage once, including usage exceeding the supplied value; absent usage SHALL remain unresolved, not become free.

#### Scenario: Manual probe reaches an upstream that rejects the parameter
- **WHEN** an administrator probes the pinned account using Luna
- **THEN** exactly one upstream request is sent without `max_output_tokens`
- **AND** the actual usage is settled without a model fallback or repeat

#### Scenario: External provider supports an output cap
- **WHEN** a Responses request with `max_output_tokens` targets an external provider
- **THEN** that field remains in its upstream request

#### Scenario: A tiny unsupported cap cannot under-reserve key budget
- **WHEN** a subscription request supplies an output cap of one token and its key has 1024 output tokens available
- **THEN** admission reserves the remaining 1024 tokens rather than reserving only one output token
- **AND** settlement replaces that reservation with actual reported usage

### Requirement: Subscription streaming accepts a missing media type only with valid SSE

A successful ChatGPT subscription HTTP response with no Content-Type SHALL be processed by the same bounded SSE parser and terminal/id/usage validation as an explicit `text/event-stream` response. An explicit incompatible media type SHALL still be rejected. External-provider streaming SHALL retain its existing Content-Type requirement. A missing media type MUST NOT make a non-SSE body, malformed event, missing terminal or unreported usage into a successful or free request.

#### Scenario: Real subscription stream omits Content-Type
- **WHEN** the subscription upstream returns HTTP 200 without Content-Type and valid SSE ending in response.completed with known usage
- **THEN** the probe or normal Responses request completes and settles the reported usage exactly once

#### Scenario: Missing header does not replace protocol validation
- **WHEN** a headerless subscription body is HTML, malformed SSE or lacks a valid terminal usage report
- **THEN** the request fails and unknown usage remains unresolved

#### Scenario: External stream omits Content-Type
- **WHEN** an external model source returns a response without Content-Type
- **THEN** the existing invalid-stream rejection remains in force
