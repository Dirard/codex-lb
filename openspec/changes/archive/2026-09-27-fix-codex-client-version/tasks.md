## 1. Client compatibility

- [x] 1.1 Add the validated persisted setting and live catalog/provider version resolution; verify default, override, migration, restart, hot-update and failure boundaries.
- [x] 1.2 Add the version-gated failure to the authenticated administrative probe regression; verify one Luna request, no fallback, and unchanged known/unknown accounting.
- [x] 1.3 Add the Settings form and validate save payload, invalid input and read-only behavior in frontend tests and the browser.

## 2. Verification and local update

- [x] 2.1 Run focused race tests, the full Go suite, go vet and the build; sync and validate the OpenSpec contract and design.
- [x] 2.2 Back up and replace the authorized local binary; verify readiness, data preservation and free catalog refresh, then archive the verified change without a live paid probe.
