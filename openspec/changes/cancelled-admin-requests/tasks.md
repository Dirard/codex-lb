## 1. Implementation

- [x] 1.1 Admin error mapping answers 499 `request_cancelled` without an error log for `context.Canceled`.

## 2. Verification

- [x] 2.1 Route regression test: a cancelled `/api/accounts` read gives 499 and no `admin operation failed` line, while another failure still gives 500 with the line.
- [ ] 2.2 Gates and strict OpenSpec validation.
