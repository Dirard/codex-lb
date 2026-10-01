package application

import (
	"context"
	"encoding/json"
	"errors"

	"codex-lb/internal/domain"
)

func (s *CodexOperations) activeKey(ctx context.Context, keyID string) (domain.APIKey, error) {
	key, err := s.store.GetAPIKey(ctx, keyID)
	if err != nil || !key.IsActive || key.ExpiresAt != nil && !key.ExpiresAt.After(s.now()) {
		return domain.APIKey{}, &ProxyError{Code: "invalid_api_key", Status: 401, Message: "Invalid or expired API key"}
	}
	return key, nil
}

func (s *CodexOperations) resolveConversationOwner(ctx context.Context, options CodexOperationOptions) (resolvedConversation, error) {
	resolved, err := resolveConversation(ctx, s.store, options.KeyID, "", options.identity(), s.now())
	if err == nil {
		err = resolved.validateFileOwner("", 0, 0)
	}
	return resolved, err
}

func (s *CodexOperations) accountForOwner(ctx context.Context, keyID string, owner *domain.Continuation, fileOwner *CodexResourceOwner) (domain.Account, error) {
	if owner != nil && fileOwner != nil && (owner.AccountID != fileOwner.AccountID || owner.AccountGeneration != fileOwner.AccountGeneration || owner.RouteRevision != fileOwner.RouteRevision) {
		return domain.Account{}, &ProxyError{Code: "file_owner_conflict", Status: 409, Message: "Conversation and file owners differ"}
	}
	if owner != nil {
		account, err := s.strictOwnerAccount(ctx, keyID, owner.AccountID, owner.AccountGeneration, owner.RouteRevision)
		if err == nil && account.Provider != owner.ProviderID {
			err = ownerUnavailable()
		}
		return account, err
	}
	if fileOwner != nil {
		return s.strictOwnerAccount(ctx, keyID, fileOwner.AccountID, fileOwner.AccountGeneration, fileOwner.RouteRevision)
	}
	accounts, err := s.store.EligibleAccounts(ctx, keyID)
	if err != nil {
		return domain.Account{}, err
	}
	for _, account := range accounts {
		if account.Kind == domain.AccountChatGPT && account.Status == domain.AccountActive {
			return account, nil
		}
	}
	return domain.Account{}, domain.ErrNoAccounts
}

func (s *CodexOperations) compactAccount(ctx context.Context, options CodexOperationOptions, key domain.APIKey, owner *domain.Continuation, fileOwner *CodexResourceOwner, body json.RawMessage, modelEnforced bool, settings domain.RuntimeSettings) (domain.Account, json.RawMessage, *accountLease, *requestAffinity, error) {
	request := responseRequest{TierEnforced: key.EnforcedServiceTier != nil, ModelEnforced: modelEnforced, CompactionTrigger: true}
	_ = json.Unmarshal(body, &request.Object) // prepareCompact already validated these fields.
	_ = json.Unmarshal(request.Object["model"], &request.Model)
	_ = json.Unmarshal(request.Object["service_tier"], &request.Tier)
	_ = json.Unmarshal(request.Object["input"], &request.Input)
	request.ResponsesLite = inputUsesResponsesLite(request.Input)
	var account domain.Account
	var lease *accountLease
	var affinity *requestAffinity
	var err error
	if owner != nil || fileOwner != nil {
		account, err = s.accountForOwner(ctx, key.ID, owner, fileOwner)
		if err != nil {
			return domain.Account{}, nil, nil, nil, err
		}
		_, _, err = filterCatalogAccounts(s.Catalog, &request, []domain.Account{account}, account.ID, true)
	} else {
		if s.selectCompact == nil {
			return domain.Account{}, nil, nil, nil, &ProxyError{Code: "compact_routing_unavailable", Status: 503, Message: "Compact account selection is not configured"}
		}
		affinity, err = lookupRequestAffinity(ctx, s.store, key.ID, options.identity(), options.ClientAffinity, request, settings)
		if err != nil {
			return domain.Account{}, nil, nil, nil, err
		}
		account, lease, err = s.selectCompact(ctx, key.ID, &request, settings, domain.Account{}, nil, affinity.preferredAccountID())
	}
	if err != nil {
		return domain.Account{}, nil, nil, nil, err
	}
	body, err = json.Marshal(request.Object)
	if err != nil {
		lease.release()
		lease = nil
	}
	return account, body, lease, affinity, err
}

func (s *CodexOperations) strictOwnerAccount(ctx context.Context, keyID, accountID string, generation, revision int64) (domain.Account, error) {
	// The narrow scope check intentionally does not use EligibleAccounts:
	// that selector excludes exhausted accounts even when an established
	// ChatGPT continuation remains entitled to its owner.
	account, err := s.store.ScopedAccountForOwner(ctx, keyID, accountID, s.now())
	if err != nil || account.ID != accountID || account.Generation != generation || account.RouteRevision != revision ||
		account.RequiresEgressDecision ||
		account.Status != domain.AccountActive && account.Status != domain.AccountRateLimited && account.Status != domain.AccountQuotaExceeded {
		return domain.Account{}, ownerUnavailable()
	}
	return account, nil
}

func (s *CodexOperations) selectTranscriptionAccount(ctx context.Context, keyID, model string) (domain.Account, domain.ModelSourceModel, error) {
	accounts, err := s.store.EligibleAccounts(ctx, keyID)
	if err != nil {
		return domain.Account{}, domain.ModelSourceModel{}, err
	}
	sources, err := s.store.ListModelSources(ctx)
	if err != nil {
		return domain.Account{}, domain.ModelSourceModel{}, err
	}
	sourceByID := make(map[string]domain.ModelSource, len(sources))
	for _, source := range sources {
		sourceByID[source.ID] = source
	}
	for _, account := range accounts {
		if account.Status != domain.AccountActive || account.RequiresEgressDecision {
			continue
		}
		if account.Kind == domain.AccountChatGPT {
			if model == transcriptionModel {
				return account, domain.ModelSourceModel{}, nil
			}
			continue
		}
		source, ok := sourceByID[account.ID]
		if ok && source.Enabled && source.Audio {
			if sourceModel, supported := source.Model(model); supported {
				return account, sourceModel, nil
			}
		}
	}
	return domain.Account{}, domain.ModelSourceModel{}, domain.ErrNoAccounts
}

func (s *CodexOperations) singleFileOwner(ctx context.Context, keyID string, owner *domain.Continuation, body json.RawMessage) (*CodexResourceOwner, []string, error) {
	ids := collectFileIDs(body)
	var result *CodexResourceOwner
	for _, id := range ids {
		fileOwner, err := s.owners.GetCodexResourceOwner(ctx, CodexResourceFile, id, keyID, s.now())
		if errors.Is(err, domain.ErrNotFound) {
			if owner == nil {
				return nil, nil, &ProxyError{Code: "file_owner_not_found", Status: 409, Message: "File ownership is unavailable for input file"}
			}
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		if result != nil && (result.AccountID != fileOwner.AccountID || result.AccountGeneration != fileOwner.AccountGeneration || result.RouteRevision != fileOwner.RouteRevision) {
			return nil, nil, &ProxyError{Code: "file_owners_conflict", Status: 409, Message: "Input files belong to different accounts"}
		}
		ownerCopy := fileOwner
		result = &ownerCopy
	}
	return result, ids, nil
}
