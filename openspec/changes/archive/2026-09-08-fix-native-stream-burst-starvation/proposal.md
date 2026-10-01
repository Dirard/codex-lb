## Why

The native helper reader can enqueue a buffered burst without yielding. A ready
consumer then overflows the 64-event queue before it can drain it, aborting a
healthy stream with `consumer_backpressure`.

## What Changes

Yield to stream consumers after enqueueing each event. Keep the queue limit,
overflow cancellation, request isolation, and transport selection unchanged.

## Impact

Native HTTP and WebSocket event dispatch; no configuration or database changes.
