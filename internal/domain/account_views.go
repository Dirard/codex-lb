package domain

import "time"

// AccountUsageWindowDuration is the trailing window of the accounts list's
// usage figures ("Last 7 days", dashboard redesign use case 8).
const AccountUsageWindowDuration = 7 * 24 * time.Hour

// AccountView is one non-deleted account with everything the accounts list
// shows, read in one snapshot (dashboard redesign use case 8).
type AccountView struct {
	Account          Account
	Quotas           []AccountQuota
	Credits          *AccountCreditStatus
	AdditionalQuotas []AccountAdditionalQuota
	// RefusalAt is the latest quota refusal; it is read only for
	// quota-exceeded accounts, the only status it can change.
	RefusalAt *time.Time
	GroupIDs  []string
	// Totals are the lifetime usage totals of the account.
	Totals UsageTotals
	// Credential holds the token ciphertexts only (no external key); nil
	// when the account has no stored credential.
	Credential *AccountCredential
	// Usage7d covers the AccountUsageWindowDuration ending at the read.
	Usage7d AccountUsageWindow
}

// AccountUsageWindow sums the requests the reports count over a window.
type AccountUsageWindow struct {
	Requests     int64
	InputTokens  int64
	OutputTokens int64
	CostUSD      float64
}

// AccountViews is the accounts list snapshot.
type AccountViews struct {
	Accounts []AccountView
	// RollupsUsable reports whether Usage7d came from the rollups.
	RollupsUsable bool
}
