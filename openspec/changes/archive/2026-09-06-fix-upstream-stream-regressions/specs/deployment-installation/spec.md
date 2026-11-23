## MODIFIED Requirements

### Requirement: Official Linux container packages locked native egress

The official Linux container build MUST compile the native egress worker from
the repository-root Cargo workspace with its committed lockfile and pinned
toolchain in an isolated Rust build stage, and MUST install only the resulting
release executable as `codex-lb-native-egress` on the runtime path. The
executable MUST support a long-lived multiplexed request protocol and reusable
reqwest client pools without requiring a sidecar or operator setting. The
runtime image MUST NOT contain the Rust toolchain or Cargo build directory.
Python wheel and source installs MUST remain valid when the executable is
absent. Development Compose watch MUST rebuild the server image when the root
`Cargo.toml`, `Cargo.lock`, or files under `crates/` change, so the running
helper matches the source workspace.

#### Scenario: Container runtime exposes native helper

- **WHEN** the official Linux image is built from the repository
- **THEN** `codex-lb-native-egress` is executable on the runtime path
- **AND** it was built with the committed lockfile
- **AND** it accepts multiple request commands during one process lifetime
- **AND** Cargo and the Rust compiler are absent from the runtime image

#### Scenario: Universal Python package remains portable

- **WHEN** a wheel or source install runs on a platform without the helper
- **THEN** importing and starting codex-lb succeeds
- **AND** supported direct requests fall back to the Python transport

#### Scenario: Development watch refreshes the compiled helper

- **WHEN** a Cargo manifest, lockfile, or native crate source changes while development Compose watch is active
- **THEN** the server image is rebuilt instead of only syncing Python source onto the old helper
