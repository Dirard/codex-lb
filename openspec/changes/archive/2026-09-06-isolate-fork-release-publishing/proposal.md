## Why

Fork releases must not invoke the original project's PyPI publishing or failed-release withdrawal pipeline. This fork publishes its own source release and builds its local deployment image.

## What Changes

- Skip the upstream Release pipeline for tags prefixed with `fork-`.
- Keep all existing stable and beta release behavior unchanged.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `release-management`: isolate manually published fork releases from upstream artifact publishing.

## Impact

One workflow admission condition and a workflow-contract regression. No application, database, or dependency changes.
