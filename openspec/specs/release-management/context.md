# Fork release publishing

Manual fork source releases use a `fork-` tag such as `fork-2026.09.06.1`.
The operator verifies and builds the deployment image; upstream PyPI and Helm
publication do not belong to that release. The workflow skips these tags at
metadata admission, without disabling Actions or changing stable/beta behavior.
Publish only after the local image and database compatibility checks succeed.
See [spec.md](spec.md) for the publishing contract.
