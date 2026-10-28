package domain

// ResponseFinalization is everything one Responses attempt persists after its
// upstream call returned. The store commits it as one durable write, before the
// client sees the terminal event.
type ResponseFinalization struct {
	ReservationID string
	// RetainUncertain keeps the reservation for reconciliation instead of
	// settling it; Settlement is ignored then.
	RetainUncertain bool
	Settlement      UsageSettlement
	// Outcome is recorded only after the ledger step succeeded.
	Outcome *AccountOutcome
	// Continuation is the response owner; Aliases are its session and
	// turn-state aliases fenced by ReservationID. Both are saved only after the
	// ledger and outcome steps succeeded.
	Continuation *Continuation
	Aliases      []Continuation
	Bounds       ContinuationBounds
}

// AccountOutcome is the provider health result of a settled or retained attempt.
type AccountOutcome struct {
	AccountID    string
	QuotaRefused bool
	Success      bool
	// QuotaMark marks the key's previous response quota-refused on this account.
	QuotaMark *ContinuationQuotaMark
}

type ContinuationQuotaMark struct {
	KeyID      string
	ResponseID string
}

// ResponseFinalizationResult reports isolated steps. The ledger step never
// rolls back because of a later step; a ledger failure is the call's error.
type ResponseFinalizationResult struct {
	// Retained is false when RetainUncertain found no open reservation to keep.
	Retained        bool
	OutcomeErr      error
	ContinuationErr error
}
