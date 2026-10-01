package domain

import "errors"

var (
	ErrNotFound                = errors.New("not found")
	ErrConflict                = errors.New("conflict")
	ErrInvalid                 = errors.New("invalid data")
	ErrLimitReached            = errors.New("API key limit reached")
	ErrNoAccounts              = errors.New("no eligible accounts")
	ErrPlanConfirmationPending = errors.New("plan downgrade confirmation pending")
)
