# File and pasted auth.json import

- Last edited with skill pack: `0.2.2`

## ADDED Requirements

### Requirement: Dashboard account import accepts a file or pasted auth JSON

The account import dialog SHALL offer file selection and manual JSON paste as explicit alternatives, with file selection as the default. Both modes SHALL use the same authenticated account-import endpoint and server credential validation. Pasted content SHALL be sent as one UTF-8 file part named `auth_json` with filename `auth.json` and media type `application/json`. Empty input and files exceeding 1 MiB SHALL be rejected before dispatch. Import controls SHALL prevent another submission while busy. Failed imports SHALL keep the draft available for correction and display safe error feedback without an unhandled rejection. Changing modes, closing the dialog or completing import SHALL clear the corresponding temporary credential draft; reopening SHALL start empty. The UI MUST NOT read clipboard contents automatically or persist/log the credential draft outside the import flow.

#### Scenario: Import a local auth.json file
- **WHEN** an operator selects a file and submits it
- **THEN** the selected file reaches the existing import operation unchanged
- **AND** success clears the draft and closes the dialog

#### Scenario: Paste auth.json from the clipboard
- **WHEN** an operator chooses the paste mode and pastes a nonempty document
- **THEN** explicit submission sends that exact document through the same file-import contract
- **AND** normal keyboard/context-menu paste works without Clipboard API permission

#### Scenario: Invalid or oversized input
- **WHEN** the input is empty, exceeds the file limit or is rejected by the existing import operation
- **THEN** no account is reported as imported and the dialog remains available with appropriate safe feedback
- **AND** validation messages do not echo credential contents

#### Scenario: Close or change the import method
- **WHEN** the operator changes the mode or closes and reopens the import dialog
- **THEN** the previous mode's credential draft is discarded and cannot be accidentally submitted
