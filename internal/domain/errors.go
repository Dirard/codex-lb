package domain

import "errors"

var (
	ErrNotFound                = errors.New("not found")
	ErrConflict                = errors.New("conflict")
	ErrInvalid                 = errors.New("invalid data")
	ErrLimitReached            = errors.New("API key limit reached")
	ErrNoAccounts              = errors.New("no eligible accounts")
	ErrPlanConfirmationPending = errors.New("plan downgrade confirmation pending")
	// ErrAffinityPersistence marks a soft-affinity write failure that rolled
	// back the reservation it shared a transaction with.
	ErrAffinityPersistence = errors.New("affinity persistence failed")
)
