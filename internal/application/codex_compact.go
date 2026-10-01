package application

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"codex-lb/internal/domain"
)

// CompactWithOptions settles each attempt before considering a quota-only
// account replacement, while retaining the original request's capacity lease.
func (s *CodexOperations) CompactWithOptions(ctx context.Context, options CodexOperationOptions, body json.RawMessage) (CodexOperationResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	key, err := s.activeKey(ctx, options.KeyID)
	if err != nil {
		return CodexOperationResult{}, err
	}
	settings, err := s.store.LoadSettings(ctx)
	if err != nil {
		return CodexOperationResult{}, err
	}
	prepared, previous, err := prepareCompact(ctx, body, key, settings, options.CodexResponsesTrigger)
	if err != nil {
		return CodexOperationResult{}, err
	}
	started := s.now()
	release, err := s.acquire(ctx)
	if err != nil {
		return CodexOperationResult{}, err
	}
	defer release()
	resolved, err := resolveConversation(ctx, s.store, key.ID, previous, options.identity(), s.now())
	if err != nil {
		return CodexOperationResult{}, err
	}
	owner := resolved.Owner
	fileOwner, _, err := s.singleFileOwner(ctx, key.ID, owner, prepared)
	if err != nil {
		return CodexOperationResult{}, err
	}
	fileOwnerID := ""
	if fileOwner != nil {
		fileOwnerID = fileOwner.AccountID
	}
	fileOwnerGeneration := int64(0)
	fileOwnerRevision := int64(0)
	if fileOwner != nil {
		fileOwnerGeneration = fileOwner.AccountGeneration
		fileOwnerRevision = fileOwner.RouteRevision
	}
	if err := resolved.validateFileOwner(fileOwnerID, fileOwnerGeneration, fileOwnerRevision); err != nil {
		return CodexOperationResult{}, err
	}
	modelEnforced := key.EnforcedModel != nil && (!options.CodexResponsesTrigger || key.ApplyToCodexModel)
	account, prepared, lease, affinity, err := s.compactAccount(ctx, options, key, owner, fileOwner, prepared, modelEnforced, settings)
	if err != nil {
		return CodexOperationResult{}, err
	}
	defer func() { lease.release() }()
	if account.Kind != domain.AccountChatGPT {
		return CodexOperationResult{}, ownerUnavailable()
	}
	excluded := map[string]bool{}
	replayed := false
	var replay json.RawMessage
	var reservationID string
	for {
		if err := ctx.Err(); err != nil {
			return CodexOperationResult{}, err
		}
		alreadyRefused := !replayed && owner != nil && owner.QuotaRefused
		if !alreadyRefused {
			target := CodexOperationTarget{Account: account, KeyID: key.ID,
				admissionLease: lease,
				ConfirmedOwner: !replayed && (fileOwner != nil || owner != nil && account.ID == owner.AccountID)}
			if !replayed {
				target.affinity = affinity
				target.SessionID = options.upstreamSessionID(account.ID)
				if fileOwner != nil || owner != nil && account.ID == owner.AccountID {
					target.TurnState = resolved.UpstreamTurnState
				}
			}
			result, callErr := s.billedAdmitted(ctx, options, key, account, modelFromCompact(prepared), "compaction", target, func(attemptReservationID string, target CodexOperationTarget) (CodexOperationResult, error) {
				reservationID = attemptReservationID
				result, err := s.provider.Compact(ctx, target, prepared)
				if options.CodexResponsesTrigger && err == nil && !result.Failed && result.Status >= 200 && result.Status < 300 {
					var item json.RawMessage
					result.Body, item = NormalizeCodexCompactOutput(result.Body)
					if item == nil {
						return result, &ProviderFailure{Code: "invalid_upstream_response", Status: 502, Dispatched: true}
					}
					result.Body = compactResponseID(result.Body)
				}
				return result, err
			}, nil, prepared, fileOwner != nil || owner != nil && owner.FilePinned, started)
			lease = nil // billedAdmitted released this attempt before any replay.
			if !compactQuotaRefusal(result, callErr) {
				return result, callErr
			}
			if owner != nil && !replayed {
				cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				err := s.store.MarkContinuationQuotaRefused(cleanup, key.ID, owner.ResponseID, owner.AccountID, reservationID)
				cancel()
				if err != nil {
					return CodexOperationResult{}, err
				}
			}
			if result.OutputObserved || result.UsageReported && !result.UsageKnown || result.UsageKnown && (result.Usage.InputTokens != 0 || result.Usage.OutputTokens != 0) {
				return result, callErr // Quota proof does not authorize a second billed compact.
			}
		}
		if fileOwner != nil || owner != nil && owner.FilePinned {
			return CodexOperationResult{}, replayUnavailable()
		}
		if owner != nil {
			if _, err := s.strictOwnerAccount(ctx, key.ID, owner.AccountID, owner.AccountGeneration, owner.RouteRevision); err != nil {
				return CodexOperationResult{}, err
			}
		}
		if replay == nil {
			replay, err = s.compactReplay(ctx, prepared, body, owner)
			if err != nil {
				return CodexOperationResult{}, err
			}
		}
		excluded[account.ID] = true
		request, err := parseResponse(replay, key, settings, options.CodexResponsesTrigger)
		if err != nil {
			return CodexOperationResult{}, err
		}
		request.CompactionTrigger = true // Compact is subscription-only.
		request.ResponsesLite = inputUsesResponsesLite(request.Input)
		if s.selectCompact == nil {
			return CodexOperationResult{}, &ProxyError{Code: "compact_routing_unavailable", Status: 503, Message: "Compact account selection is not configured"}
		}
		account, lease, err = s.selectCompact(ctx, key.ID, &request, settings, account, excluded)
		if err != nil {
			return CodexOperationResult{}, err
		}
		prepared, _ = json.Marshal(request.Object)
		replayed = true
	}
}

func compactQuotaRefusal(result CodexOperationResult, err error) bool {
	if quotaFailure(ResponseResult{Failed: result.Failed, ErrorCode: result.ErrorCode}, nil) {
		return true
	}
	if err != nil {
		var failure *ProviderFailure
		return errors.As(err, &failure) && failure.QuotaRefused
	}
	return false
}
