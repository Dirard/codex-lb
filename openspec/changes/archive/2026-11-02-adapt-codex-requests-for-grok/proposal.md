## Why

After go-v1.11.0 the owner created a Grok subscription source on prod, and the first Codex request failed before dispatch with `unsupported_capability`. Codex CLI 0.165.4 does not read codex-lb's catalog. It sends its own reasoning settings (`effort: "max"`, `summary: "detailed"`), its MCP and agent tools as `namespace` tools, and `{"type":"web_search","external_web_access":false}`.

Six owner-approved probes from prod showed which of these xAI refuses:
- `namespace` tools: 422;
- `external_web_access`: 400;
- `effort: "max"`: 400;
- any effort on `grok-build-0.1`: 400.

The same request with flat functions, a plain web search and effort `xhigh` answered 200.

## What Changes

- Requests to a `grok_subscription` source have their `namespace` tools flattened to functions named `<namespace>-<child>`. Calls in the answer are mapped back to the child name and its namespace.
- `web_search` keeps only the fields xAI supports. A cached-only web search (`external_web_access: false`) is left out, and the hosted web search tool is allowed for the kind.
- `reasoning.effort` is fitted to the model: `grok-4.7` and `grok-4.6` take up to `xhigh`, and `grok-4.5` up to `high`. Other models get no `reasoning` object.
- New sources advertise those efforts in the catalog.

## Capabilities

### Modified Capabilities

- `go-runtime`: «A Grok subscription account can back a model source» gains the request adaptation.

## Impact

- No schema, API or dashboard change.
- Sources created on go-v1.11.0 work without being recreated; only their catalog lacks the effort levels.
