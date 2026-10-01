package application

import (
	"context"
	"time"

	"codex-lb/internal/domain"
)

// ScopedOwnerAccounts authorizes an established owner against current key and
// account policy without applying new-session quota selection.
type ScopedOwnerAccounts interface {
	ScopedAccountForOwner(context.Context, string, string, time.Time) (domain.Account, error)
}
