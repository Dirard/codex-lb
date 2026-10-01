# Import auth.json from a file or pasted JSON

- Last edited with skill pack: `0.2.2`

## Why

The account import dialog accepts only a local file. Operators also need to paste
an auth.json document directly without saving sensitive credentials to a file.

## What Changes

- Offer file selection and manual clipboard paste as two explicit import modes.
- Submit both modes through the existing file-based account import endpoint.
- Clear temporary credentials when closing, succeeding or changing mode; retain safe validation and failure feedback.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `go-runtime`: dashboard account import accepts a file or pasted auth.json.

## Impact

Account import dialog, existing UI tests and translations. No new backend endpoint,
dependency, database migration or running-service update.
