package application

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"codex-lb/internal/domain"
)

type conversationIdentity struct {
	SessionID, ThreadID, TurnState string
	SynthesizedTurnState           bool
}

type resolvedConversation struct {
	Owner             *domain.Continuation
	UpstreamTurnState string
	UnknownTurnState  bool
}

func (options ResponseOptions) identity() conversationIdentity {
	return conversationIdentity{options.SessionID, options.ThreadID, options.TurnState, options.SynthesizedTurnState}
}

func (options CodexOperationOptions) identity() conversationIdentity {
	return conversationIdentity{options.SessionID, options.ThreadID, options.TurnState, options.SynthesizedTurnState}
}

func (identity conversationIdentity) upstreamSessionID(keyID, accountID string) string {
	if identity.SessionID == "" && identity.ThreadID == "" {
		return ""
	}
	session := identity.SessionID
	if identity.ThreadID != "" {
		session = logicalSessionLookupID(identity.SessionID, identity.ThreadID)
	}
	return secretTag([]byte(keyID + "\x00" + accountID + "\x00" + session))
}

func (options CodexOperationOptions) upstreamSessionID(accountID string) string {
	if options.ThreadID == "" {
		return options.SessionID // Preserve the legacy ancillary bare-session wire contract.
	}
	return options.identity().upstreamSessionID(options.KeyID, accountID)
}

func logicalSessionLookupID(session, thread string) string {
	if thread == "" {
		if session == "" {
			return ""
		}
		return sessionLookupID(session)
	}
	framed, _ := json.Marshal([]string{session, thread})
	return fmt.Sprintf("session:thread:%x", sha256.Sum256(framed))
}

func turnStateLookupID(state string) string {
	return fmt.Sprintf("session:turn:%x", sha256.Sum256([]byte(state)))
}

// resolveConversation compares independent hard aliases before locality/routing.
// File ownership is checked by the caller once its operation's file pins resolve.
func resolveConversation(ctx context.Context, store Continuations, keyID, previous string, identity conversationIdentity, now time.Time) (resolvedConversation, error) {
	result := resolvedConversation{}
	if len(identity.SessionID) > 4096 || len(identity.ThreadID) > 4096 || len(identity.TurnState) > 8192 {
		return result, &ProxyError{Code: "invalid_request", Status: 400, Message: "Conversation identity is too large"}
	}
	if !identity.SynthesizedTurnState {
		result.UpstreamTurnState = identity.TurnState
	}
	type identitySource struct {
		id       string
		previous bool
		turn     bool
	}
	sources := []identitySource{{previous, true, false}, {logicalSessionLookupID(identity.SessionID, identity.ThreadID), false, false}}
	if strings.TrimSpace(identity.TurnState) != "" {
		sources = append(sources, identitySource{turnStateLookupID(identity.TurnState), false, true})
	}
	for _, source := range sources {
		if source.id == "" {
			continue
		}
		owner, err := store.GetContinuation(ctx, keyID, source.id, now)
		if errors.Is(err, domain.ErrNotFound) {
			if source.previous {
				return result, &ProxyError{Code: "previous_response_not_found", Status: 409, Message: "Previous response context is unavailable; resend the full conversation without previous_response_id"}
			}
			if source.turn && !identity.SynthesizedTurnState {
				result.UnknownTurnState = true
			}
			continue
		}
		if err != nil {
			return result, err
		}
		if result.Owner != nil && (result.Owner.AccountID != owner.AccountID || result.Owner.AccountGeneration != owner.AccountGeneration || result.Owner.RouteRevision != owner.RouteRevision || result.Owner.ProviderID != owner.ProviderID) {
			return result, &ProxyError{Code: "conversation_owner_conflict", Status: 409, Message: "Conversation identity sources have different owners"}
		}
		if result.Owner == nil {
			result.Owner = &owner
		}
		if source.turn && !owner.TurnStateForwardable {
			result.UpstreamTurnState = ""
		}
	}
	return result, nil
}

func (resolved resolvedConversation) validateFileOwner(accountID string, generation, revision int64) error {
	if accountID != "" && resolved.Owner != nil && (resolved.Owner.AccountID != accountID || resolved.Owner.AccountGeneration != generation || resolved.Owner.RouteRevision != revision) {
		return &ProxyError{Code: "file_owner_conflict", Status: 409, Message: "Conversation and input file owners differ"}
	}
	if resolved.UnknownTurnState && resolved.Owner == nil && accountID == "" {
		return &ProxyError{Code: "conversation_owner_required", Status: 409, Message: "Turn-state ownership is unavailable"}
	}
	return nil
}

// rememberConversationAliases publishes only accounted ownership, keeping the
// existing reservation fence across logical-thread and turn-state aliases.
func rememberConversationAliases(ctx context.Context, store Continuations, owner domain.Continuation, identity conversationIdentity, forwardTurn bool, reservationID string, bounds domain.ContinuationBounds) error {
	if id := logicalSessionLookupID(identity.SessionID, identity.ThreadID); id != "" {
		alias := owner
		alias.ResponseID = id
		if err := store.SaveSessionContinuation(ctx, alias, reservationID, bounds); err != nil {
			return err
		}
	}
	if strings.TrimSpace(identity.TurnState) != "" {
		alias := owner
		alias.ResponseID = turnStateLookupID(identity.TurnState)
		alias.TurnStateForwardable = forwardTurn && !identity.SynthesizedTurnState
		return store.SaveSessionContinuation(ctx, alias, reservationID, bounds)
	}
	return nil
}
