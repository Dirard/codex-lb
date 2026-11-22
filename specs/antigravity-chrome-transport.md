# Antigravity Chrome 123 transport

- Status: implemented (2026-11-22)
- Owner decision: «сделай чтобы транспорт шел с профилем Chrome 123 для запросов антигравити как в менеджере» — https://github.com/lbjlaq/Antigravity-Manager (2026-11-22). Supersedes the same-day «no fingerprint emulation» boundary of `antigravity-accounts.md` for the transport layer only.
- Related: `specs/antigravity-accounts.md` (R4 records the normative requirement; its decision log records the reversal).

## Problem

Google's front ends fingerprint the client stack (TLS ClientHello, HTTP/2 settings and frame shapes, header order). codex-lb's Antigravity traffic left through the shared Go `net/http` transport, whose Go-specific fingerprint no real Antigravity client produces. Antigravity-Manager sends its upstream Google calls through `rquest` with `Emulation::Chrome123`, keeping its own `antigravity/hub/...` User-Agent. The owner wants the same transport profile here.

## What the manager actually does (research, 2026-11-22)

- `src-tauri/src/proxy/upstream/client.rs`: `rquest::Client::builder().emulation(rquest_util::Emulation::Chrome123)` — TLS + HTTP/2 + header order emulation, `tcp_keepalive(30s)`, `pool_idle_timeout(15s)`.
- `rquest-util 2.2.1` `Emulation::Chrome123` (used by the manager, verified from the published crate): TLS = `permute_extensions(true)`, `pre_shared_key(true)`, `enable_ech_grease(true)`, curves `X25519, SECP256R1, SECP384R1`, the standard Chrome cipher and sigalg lists, TLS 1.2–1.3, ALPS `h2` old codepoint, Brotli cert compression, GREASE. HTTP/2 = stream window `6291456`, connection window `15728640` (increment `15663105`), `header_table_size 65536`, `max_header_list_size 262144`, `enable_push=false`, pseudo order `:method,:authority,:scheme,:path`.
- The User-Agent on Cloud Code calls is NOT Chrome: it stays `antigravity/hub/<v> (aidev_client; os_type=windows; arch=amd64; cl=1023160026)` — the form codex-lb already sends (commit 9a3ec581).

## Solution

- `internal/adapters/chromeprofile`: an `http.RoundTripper` over `github.com/bogdanfinn/tls-client` v1.16.0 (v1.17+ requires Go 1.27, above the repo's 1.26 toolchain; v1.16.0 carries the same Chrome_120 capture and options). Profile: the captured `Chrome_120` with `WithRandomTLSExtensionOrder()` — wire-identical to rquest's Chrome123 (Chrome 120–123 emit the same ClientHello and HTTP/2 fingerprint; real Chrome randomizes extension order, which the option reproduces; the capture's settings `1:65536, 2:0, 4:6291456, 6:262144`, connection flow `15663105` and pseudo order equal the rquest values above). Rebuilt with `allowHTTP=true` so plain-HTTP endpoints (loopback stubs, self-hosted sources) still work — a fingerprint only exists on a TLS connection. Requests carry the manager's 7200 s bound (`WithTimeout`); the library treats 0 as an instantly expired dial deadline, not "no timeout".
- The transport adds, rewrites and orders no headers: the request travels verbatim; the fingerprint is purely TLS/HTTP-2.
- Egress guard and proxies: the runtime passes `newUpstreamTransport`'s `DialContext` and `Proxy` into the transport. Restricted destinations stay blocked at dial time; proxied destinations keep their name check. Per-proxy tls-clients are cached (bounded at 4 endpoints, cache restarts beyond that).
- Wiring (`cmd/codex-lb/runtime.go`): one `antigravityClient := &http.Client{Transport: chromeprofile.Transport}` serves the whole Google side — `antigravity.New` (OAuth token/userinfo and Cloud Code control calls) and `provider.Config.AntigravityHTTPClient` → `upstream.HTTPAdapter.SetAntigravityClient` → `openAntigravity` (model traffic). Other providers keep the shared transport; nothing else changes fingerprints.
- Redirects stay refused (`http.ErrUseLastResponse`) on both paths.

## Boundaries kept

- Only Antigravity Google traffic changes transport. OpenAI/Z.AI/ChatGPT/Grok keep the shared `net/http` client.
- No new headers, no `sec-ch-ua`, no Chrome User-Agent on Cloud Code calls, no native-only request fields, no prompt rewriting.
- Security invariants: tokens never in logs (transport errors are replaced with a host-only message unless they are stdlib `*url.Error`); credentials still only to the configured endpoints; egress restriction still enforced.

## Tests

- `chromeprofile`: request verbatim round trip (headers, body, no additions), proxy routing, context cancellation, TLS end-to-end (test-only insecure endpoint), profile constants pinned to the manager's fingerprint.
- `upstream`: `TestAntigravityRequestsUseTheDedicatedClient` pins that Cloud Code calls leave through the dedicated client and never the shared one.

## Checklist

- [x] `internal/adapters/chromeprofile` with tests
- [x] `upstream.SetAntigravityClient` + `openAntigravity` routing + regression test
- [x] `provider.Config.AntigravityHTTPClient`
- [x] `cmd/codex-lb/runtime.go` wiring (egress guard dial + proxy)
- [x] `specs/antigravity-accounts.md` R4, boundary note, decision log, storage default
- [x] Fallout of 9a3ec581 repaired: unused `runtime` import, stale UA/default assertions (antigravity, provider, sqlite, web schema default), schema V54 seed correction (2.34.0 → 2.33.0)
- [x] `make check`, `make web-check`
