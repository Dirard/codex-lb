# Context: optimize-dashboard-delivery

Implementation plan: `specs/dashboard-delivery.md`. Source audit: `specs/audits/2026-10-28/06-frontend.md` (WEB-1…WEB-31).

## Decisions

- **Precompressed, not on-the-fly.** The Vite build writes `.br` (quality 11) and `.gz` (level 9) siblings; `internal/adapters/webui` only negotiates. Gzip stays because browsers advertise `br` only in secure contexts, and codex-lb is usually reached over plain HTTP on a LAN (`Accept-Encoding: gzip, deflate`). Variant responses carry `Accept-Ranges: none`; a `Range` request is always answered from the identity file.
- **ETags are content hashes.** `embed.FS` has no modification times, so every file gets a strong ETag from its bytes at startup; brotli, gzip and identity representations have distinct ETags. `index.html` is `no-cache` and revalidates with `304`.
- **Admin JSON only.** `compressJSON` wraps the authenticated admin mux, not the outer handler, so proxy routes (`/v1`, `/backend-api`, `/api/codex`), auth and key-report session routes, SSE and WebSocket upgrades never see it. Bodies below 1 KiB are sent unchanged.
- **Status endpoint.** `GET /api/dashboard/status` mirrors the overview's `lastSyncAt` rule (newest quota observation of non-deleted ChatGPT accounts) from account and quota rows only.
- **Duplicate e-mails are list-derived.** The `/api/accounts` and overview account arrays derive `isEmailDuplicate` while parsing; nothing is required from the server.
- **Removed UI** covers only fields and routes the Go server never produced; no Go behavior was removed.

## Measurements

Headless Chromium against the built binary, empty database, first load of `/dashboard`:

| | before | after (br) | after (gzip only) |
|---|---:|---:|---:|
| transferred static assets | 1921 KB | 357 KB | 395 KB |
| requests | 46 | 42 | 42 |
| recharts chunk on `/dashboard` | yes (567 KB) | no | no |

Seeded data, admin JSON on the wire: overview 12.9 KB → 2.6 KB, request logs 16.3 KB → 1.8 KB, accounts 5.0 KB → 0.9 KB.

Dashboard tests: 190.5 s sequential → 20.3 / 20.6 / 19.8 s with file parallelism (1265 tests).

## Example

```
$ curl -sI -H 'Accept-Encoding: gzip, deflate' http://127.0.0.1:2455/assets/index-<hash>.js
HTTP/1.1 200 OK
Accept-Ranges: none
Cache-Control: public, max-age=31536000, immutable
Content-Encoding: gzip
Content-Length: 65204
Content-Type: text/javascript; charset=utf-8
Etag: "<24 hex>"
Vary: Accept-Encoding
```
