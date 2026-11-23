# Probe and weekly-only quota tasks

## 1. Fix and regressions

- [x] 1.1 Default omitted probe models to gpt-6-luna and verify one pinned HTTP attempt with unchanged explicit-model and uncertain-usage behavior.
- [x] 1.2 Normalize lone weekly quota, replace complete current window sets safely, repair mislabeled weekly history and hide the false 5h legend; verify ingestion/SQLite/HTTP/UI regressions and stale/partial guards.

## 2. Verify and deploy

- [x] 2.1 Run Go/race/vet, focused UI/typecheck/build and strict specification checks; synchronize main go-runtime requirements/context.
- [x] 2.2 Back up the local installation, deploy the verified binary and verify readiness, version, quota correction and unchanged financial reservation without paid probes.
- [x] 2.3 Record the final results and archive the verified change.
