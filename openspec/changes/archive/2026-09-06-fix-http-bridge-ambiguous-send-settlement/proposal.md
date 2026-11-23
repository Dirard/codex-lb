## Why

An HTTP bridge `response.create` send can fail after dispatch and then lose all cleanup if caller cancellation interrupts the durable `unknown` write. The abandoned request keeps its queue slot and response-create gate while its ambiguous operation is not safely fenced.

## What Changes

- Give post-send ambiguous-failure settlement one cancellation-deferring task owner.
- Finish durable ambiguity marking and local request cleanup before propagating caller cancellation, without replaying the request.
- Finish a pre-dispatch operation rollback and gate cleanup before propagating cancellation, without sending the rejected request.
- Add regression coverage for cancellation during the durable write and cleanup after a failed durable write.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `responses-api-compat`: Require cancellation-safe, no-replay settlement after an ambiguous HTTP bridge send failure.

## Impact

The change is limited to HTTP bridge request submission, its unit tests, and the `responses-api-compat` delta. It adds no API, schema, configuration, or dependency.
