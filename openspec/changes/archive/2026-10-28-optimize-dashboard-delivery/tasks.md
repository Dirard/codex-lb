## 1. Delivery

- [x] 1.1 Vite plugin writes `.br`/`.gz` variants; `webui` negotiates encodings with `Vary`, `ETag`, `Range` safety and `no-cache` index; fonts move to hashed assets.
- [x] 1.2 `compressJSON` wraps the authenticated admin mux; proxy/auth/streaming routes stay untouched.
- [x] 1.3 Measure the first `/dashboard` load before and after.

## 2. Bundle and polling

- [x] 2.1 Lazy `ko`/`zh-CN` catalogues; render after i18n is ready.
- [x] 2.2 SVG usage donut and sparkline; `/dashboard` does not load recharts.
- [x] 2.3 `GET /api/dashboard/status`; status bar uses it; projections refresh on `lastSyncAt` change or every 5 minutes.

## 3. Go contract alignment

- [x] 3.1 Generation speed from `outputTokens`; client-side duplicate e-mail detection.
- [x] 3.2 Remove UI, schema fields and mocks for excluded features; Go test matches MSW routes to the Go router.

## 4. Tooling

- [x] 4.1 OAuth dialog copy button clears its reset timer; parallel vitest.
- [x] 4.2 Remove unused dependencies, vendor `shadcn/tailwind.css`, explicit TypeScript 7 typecheck.

## 5. Verification

- [x] 5.1 `npm --prefix web run lint|typecheck|test|build`, `go vet`, `golangci-lint`, `go test`, race tests, strict OpenSpec validation.
- [x] 5.2 Sync the delta into `openspec/specs/frontend-architecture/spec.md` and archive after review.
