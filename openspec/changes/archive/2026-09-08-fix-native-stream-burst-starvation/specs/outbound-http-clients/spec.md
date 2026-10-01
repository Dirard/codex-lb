## ADDED Requirements

### Requirement: Buffered native events allow ready consumers to run

The native event dispatcher MUST yield between queued events so that a runnable
stream consumer can drain its bounded queue during a buffered event burst.
Stalled consumers MUST remain bounded and MUST NOT block unrelated streams.

#### Scenario: A healthy consumer receives a buffered HTTP burst

- **GIVEN** a native helper emits a response head and more than 64 body events together
- **WHEN** the caller immediately consumes the response
- **THEN** the complete body is delivered without a consumer-backpressure error
