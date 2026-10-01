# Auth JSON compatibility tasks

## 1. Parser regression

- [x] 1.1 Reproduce the standard-token multipart rejection, implement alias resolution in the shared parser and verify canonical/legacy names, identity, encrypted token preservation, invalid inputs and export round trip.

## 2. Integration

- [x] 2.1 Run Go tests, targeted race/vet, the paste-form regression and build; keep the running service and real credentials untouched.
- [x] 2.2 Synchronize go-runtime spec/context, pass strict OpenSpec and layered validation, then archive the verified change.
