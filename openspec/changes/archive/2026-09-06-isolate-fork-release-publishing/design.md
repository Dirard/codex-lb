## Context

The inherited Release workflow publishes to the original PyPI project and withdraws incomplete public releases. A manual `fork-2026.09.06.1` release is a separate source release with a locally verified deployment image.

## Goals / Non-Goals

Isolate fork-tag publication. Do not change application versions, stable/beta publishing, or repository-wide Actions permissions.

## Decisions

Gate the existing metadata job on a non-`fork-` tag. Its dependent artifact jobs remain skipped automatically. This avoids a second publishing pipeline or disabling workflows globally.

## Risks / Trade-offs

Fork images are built and checked by the operator; the skipped upstream workflow does not claim to publish them. Only publish the manual release after local verification succeeds.
