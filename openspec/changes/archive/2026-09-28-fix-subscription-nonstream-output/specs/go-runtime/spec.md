## ADDED Requirements

### Requirement: Subscription non-streaming replies retain streamed output

Non-streaming subscription Responses and translated Chat Completions SHALL include completed output items received before the terminal event when that event has absent, null or empty output. Items SHALL retain their upstream fields and output-index ordering without duplicating indexed items. Nonempty terminal output SHALL remain authoritative. Response identity, terminal metadata and reported usage MUST be preserved, with one accounting settlement and no additional provider request. Malformed or oversized assembled output MUST fail safely without inventing usage, retrying an executed request or reporting an incomplete stream as successful. Streaming clients SHALL retain their original event flow without accumulating full output for this compatibility path.

#### Scenario: Luna completes with empty terminal output
- **WHEN** upstream emits a completed message or tool item and then a completed terminal event with empty output
- **THEN** non-streaming Responses contains the item and Chat Completions exposes its corresponding text or tool call
- **AND** actual terminal usage is settled once

#### Scenario: Terminal output is already complete
- **WHEN** the terminal event contains nonempty output alongside earlier item events
- **THEN** that terminal output is preserved without appending duplicate items

#### Scenario: Collection fails after dispatch
- **WHEN** the stream is incomplete or output collection exceeds its bound
- **THEN** the request fails without automatic replay and retains known usage or the existing uncertain-reservation behavior

### Requirement: Translated Chat streaming usage preserves upstream token counts

When a Chat Completions client requests `stream_options.include_usage` and the Responses provider reports valid usage, the translated final usage chunk SHALL preserve input, output, cached-input and reasoning counts in their corresponding Chat Completions fields, with total tokens equal to input plus output. This conversion MUST NOT change the ledger or cause another request.

#### Scenario: Subscription Responses returns snake_case usage
- **WHEN** upstream completes with nonzero `input_tokens`, `output_tokens` and token-detail fields
- **THEN** the Chat stream returns matching `prompt_tokens`, `completion_tokens`, total and detail counts rather than zeroes
