## Why

The owner reported that third-party providers answer `model_not_priced` ("No price is configured for this model and provider") although prices are set. Two defects were found:

- A model source that does not serve the requested model was counted as an unpriced candidate, so a request with no other available account got `model_not_priced` instead of "model not available".
- The model source edit dialog shows one set of settings "applied to every model listed", but a model added to the list received only the fields the operator changed. On prod `glm-5.4` was saved without prices, limits, reasoning and tools while the dialog showed those of `glm-5.3`.

## What Changes

- A source that does not serve the model is no candidate; when no permitted account serves the model the client gets 404 `model_not_available`. `model_not_priced` stays for a served model without a price.
- In the edit dialog, a model added to the list takes every setting the form shows.

## Capabilities

### Modified Capabilities

- `go-runtime`: added requirement for the unserved-model refusal.
- `frontend-architecture`: added requirement for settings of added models.

## Impact

- No schema change. Error code changes from `model_not_priced` to `model_not_available` for unserved models.
