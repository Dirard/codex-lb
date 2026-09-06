## 1. Downstream stream regression

- [x] 1.1 Restore startup HTTP error responses and structured terminal SSE errors for native clients without changing service replay decisions.
- [x] 1.2 Restore the existing native downstream keepalive path.
- [x] 1.3 Add regression coverage at native HTTP routes and the real local HTTP wire; update superseded iterator expectations.

## 2. Upstream audit and verification

- [x] 2.0 Rebuild native egress on Rust-source and Cargo-input changes in development Compose watch.
- [x] 2.1 Complete the review of upstream changes since `0615885d` through `5ad638b6` and repair additional confirmed defects within the affected contracts.
- [x] 2.2 Run relevant regression suites, synchronize the owning specifications, and validate the change and specs strictly.
