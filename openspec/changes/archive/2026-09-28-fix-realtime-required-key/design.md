# Strict Realtime key identity

- Last edited with skill pack: `0.2.2`

## Title and scope

Prevent the optional local keyless principal from replacing Realtime's required user-key identity.

## Planning anchor

`Server.proxyIngress` marks loopback/allowed CIDR requests as keyless-eligible. `authenticateProxyKey` then intentionally uses `__local_proxy__` when general auth is disabled. Realtime currently calls that function through its shared helper, unlike original `validate_required_proxy_api_key`. Amend the Go Realtime requirement only; ordinary proxy behavior is deliberate and stays.

## Connected groups or observed existing logic

The complete server adds ingress context before the operation handler. The two Realtime handlers must use existing `authenticateBearerKey`; application owner lookup and provider adapters need no changes. Standalone handler tests omit ingress context, explaining why the omission was invisible there. Test with the full `Server.Handler` and actual provider-backed operation route, a local internal principal, disabled global enforcement and two distinct user keys. No separate reverse documentation or broad mapping is needed for this narrow flow.

## Use cases

### 1. Resolve required call identity

trusted ingress request --require actual Bearer key--> authenticated user key --resolve key-scoped call owner--> authorized create or sideband

Implementation Logic:
Call `authenticateBearerKey` directly in call creation and sideband attachment. Preserve all existing expiry/revocation checks and owner scope. Do not alter `authenticateProxyKey`, local bypass configuration, administrator sessions or ordinary Responses.

Tests:
- description: Invoke canonical/slash call routes and both live aliases without a key or with an invalid key from loopback and an explicitly allowed CIDR.
  expected outcome: 401 before provider dispatch even with global enforcement off.
- description: Create a call with key A, then attach using key B through full ingress.
  expected outcome: The owner is stored under A and B receives 409 without an upstream connection.

## Implementation checklist

1. [x] Confirm the full-ingress substitution and original required-key contract.
2. [x] Replace only the two Realtime authentication calls.
3. [x] Verify full-ingress and unchanged keyless behavior; synchronize/archive specs.

## Open questions

None. The existing implementation flow authorizes this narrow security repair without a service upgrade.

## Decision log

Completeness/consistency checks preserve the general operator-controlled keyless setting and add no new auth mechanism. Existing strict Bearer validation is sufficient.

2026-09-28: Full server ingress with disabled global enforcement rejected missing/invalid keys on both create aliases and both sideband aliases from loopback and an explicitly allowed CIDR. A valid key retained its own call ownership and another valid key could not attach. Realtime and existing ordinary LocalKeyless race tests, full `go test ./...` and `go vet ./...` passed. The working service remained unchanged.
