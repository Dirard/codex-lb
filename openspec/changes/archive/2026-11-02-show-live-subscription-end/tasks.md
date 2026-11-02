## 1. Implementation

- [x] 1.1 Read `/backend-api/subscriptions` in the ChatGPT usage adapter.
- [x] 1.2 Read due accounts from the usage poller and keep the last read per account incarnation.
- [x] 1.3 Return `subscription` from `GET /api/accounts`.
- [x] 1.4 Count down to the ChatGPT read on the Accounts list with the +14-day reference.

## 2. Verification

- [x] 2.1 Adapter, application, list and dashboard tests.
- [x] 2.2 Gates, strict OpenSpec validation and release notes.
