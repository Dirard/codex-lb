## ADDED Requirements

### Requirement: Dashboard signs in and shows Antigravity accounts

The Model sources page SHALL offer adding an Antigravity account through an authorization link and a code field, and SHALL show each Antigravity source's email, tier and status, including the appeal or verification link, with re-authorization and model refresh actions next to the existing source controls.

#### Scenario: Adding an account
- **WHEN** the operator opens «Add Antigravity account», signs in at Google and pastes the code
- **THEN** the new source is listed with its email, tier and models
