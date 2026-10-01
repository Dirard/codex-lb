# Z.AI Responses implementation

## 1. Protocol selection

- [x] 1.1 Allow Z.AI Responses in domain/API validation and preserve Chat-only dispatch; verify HTTP CRUD/validation and provider regressions.
- [x] 1.2 Enable Chat/Responses controls in the shared source form without changing endpoint/credentials; verify create/edit and reducer regressions.

## 2. Verification and synchronization

- [x] 2.1 Build the binary and exercise Z.AI native JSON/SSE through the real server and SQLite against a local upstream; run Go/race/vet and focused Vitest.
- [x] 2.2 Synchronize go-runtime spec/context, validate OpenSpec and the solution design, and archive only after verification.
