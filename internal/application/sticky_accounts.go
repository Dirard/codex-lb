package application

import (
	"errors"
	"net/http"

	"codex-lb/internal/domain"
)

// UsageLimitCode is the code a client receives when the sticky-only owner of
// its conversation ran out of quota: the account limit, not a billing error.
const UsageLimitCode = "usage_limit_reached"

// stickyOwner realizes "dispatch to the owner even after an earlier quota
// mark": a conversation whose owner account is sticky-only is sent to that
// owner again, so a quota mark left by an earlier refusal (or set before the
// option was enabled) never moves it. Other owners are returned unchanged.
func stickyOwner(owner *domain.Continuation, ownerAccount domain.Account) *domain.Continuation {
	if owner == nil || !owner.QuotaRefused || !ownerAccount.StickyOnly || ownerAccount.ID != owner.AccountID {
		return owner
	}
	kept := *owner
	kept.QuotaRefused = false
	return &kept
}

// keepsStickyConversation realizes "settle it uncharged without marking the
// conversation or replaying it": an explicit quota refusal by the sticky-only
// owner of the conversation reaches the client instead of a replay elsewhere.
// A request without an owner has not started a conversation on the account
// and may still move.
func keepsStickyConversation(owner *domain.Continuation, account domain.Account) bool {
	return owner != nil && account.StickyOnly && owner.AccountID == account.ID
}

// stickyQuotaError gives the client the owner's quota refusal as a usage
// limit, the shape Codex shows as "usage limit reached", instead of a
// generic upstream failure. Other errors are returned unchanged.
func stickyQuotaError(err error) error {
	var failure *ProviderFailure
	if !errors.As(err, &failure) || !failure.QuotaRefused {
		return err
	}
	kept := *failure
	kept.Code, kept.Status = UsageLimitCode, http.StatusTooManyRequests
	return &kept
}
