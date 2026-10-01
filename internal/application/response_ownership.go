package application

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"codex-lb/internal/domain"
	"codex-lb/internal/domain/pricing"
)

func sessionLookupID(session string) string {
	return fmt.Sprintf("session:%x", sha256.Sum256([]byte(session)))
}

func (p *Proxy) resolveOwner(ctx context.Context, options ResponseOptions, request responseRequest) (resolvedConversation, responseContext, error) {
	resolved, err := resolveConversation(ctx, p.store, options.KeyID, request.Previous, options.identity(), time.Now())
	if err != nil {
		return resolved, responseContext{}, err
	}
	owner := resolved.Owner
	var history responseContext
	if request.Previous != "" && owner != nil && len(owner.ContextEncrypted) != 0 {
		plain, err := p.cipher.Decrypt(owner.ContextEncrypted)
		if err != nil {
			return resolved, history, err
		}
		if len(plain) > maxReplayBytes || json.Unmarshal(plain, &history) != nil {
			return resolved, history, errors.New("invalid stored continuation")
		}
	}
	return resolved, history, nil
}

func (p *Proxy) resolveFileOwner(ctx context.Context, keyID string, input json.RawMessage) (CodexResourceOwner, error) {
	ids := collectFileIDs(input)
	if len(ids) > 32 {
		return CodexResourceOwner{}, &ProxyError{Code: "invalid_request", Status: 400, Message: "Too many input files"}
	}
	var fileOwner CodexResourceOwner
	for _, id := range ids {
		if len(id) > 512 {
			return CodexResourceOwner{}, &ProxyError{Code: "invalid_request", Status: 400, Message: "Invalid input file ID"}
		}
		owner, err := p.store.GetCodexResourceOwner(ctx, CodexResourceFile, id, keyID, time.Now())
		if errors.Is(err, domain.ErrNotFound) {
			return CodexResourceOwner{}, &ProxyError{Code: "file_owner_not_found", Status: 409, Message: "Input file owner is unavailable"}
		}
		if err != nil {
			return CodexResourceOwner{}, err
		}
		if fileOwner.AccountID != "" && (fileOwner.AccountID != owner.AccountID || fileOwner.AccountGeneration != owner.AccountGeneration || fileOwner.RouteRevision != owner.RouteRevision) {
			return CodexResourceOwner{}, &ProxyError{Code: "file_owners_conflict", Status: 409, Message: "Input files belong to different accounts"}
		}
		fileOwner = owner
	}
	return fileOwner, nil
}

func (p *Proxy) accountCandidates(ctx context.Context, keyID string, request *responseRequest, settings domain.RuntimeSettings, route CapabilityRoute, owner *domain.Continuation, previous domain.Account, excluded map[string]bool) ([]accountCandidate, error) {
	model := request.Model
	if owner != nil && !owner.QuotaRefused {
		if _, mapped := domain.AdditionalQuotaForModel(model); mapped && canonicalModel(owner.Model) != canonicalModel(model) {
			return nil, ownerUnavailable()
		}
		scoped, err := p.store.ScopedAccountForOwner(ctx, keyID, owner.AccountID, time.Now())
		if err != nil || scoped.Provider != owner.ProviderID || scoped.Generation != owner.AccountGeneration || scoped.RouteRevision != owner.RouteRevision {
			p.retireRequiredSocket(route, owner.AccountID, keyID)
			return nil, ownerUnavailable()
		}
		if request.CompactionTrigger && scoped.Kind != domain.AccountChatGPT {
			return nil, ownerUnavailable()
		}
		if _, err := FilterCapabilityAccounts(route, []domain.Account{scoped}, owner.AccountID); err != nil {
			p.retireRequiredSocket(route, owner.AccountID, keyID)
			return nil, err
		}
		if _, _, err := p.catalogAccountsWithOmissions(request, []domain.Account{scoped}, scoped.ID); err != nil {
			p.retireRequiredSocket(route, owner.AccountID, keyID)
			return nil, err
		}
		price, err := p.ResolvePrice(ctx, scoped, model)
		if err != nil {
			return nil, err
		}
		return []accountCandidate{{account: scoped, price: price}}, nil
	}
	accounts, err := p.store.QuotaCandidateAccounts(ctx, keyID)
	if err != nil {
		return nil, err
	}
	candidates := make([]domain.Account, 0, len(accounts))
	for _, account := range accounts {
		if request.CompactionTrigger && account.Kind != domain.AccountChatGPT {
			continue
		}
		if excluded[account.ID] || owner != nil && owner.AccountID == account.ID {
			continue
		}
		if previous.ID != "" && (previous.Provider != account.Provider || previous.Kind != account.Kind) {
			continue
		}
		if previous.Kind == domain.AccountExternal && previous.BaseURL != account.BaseURL {
			continue // A compatible protocol does not make unrelated endpoints one provider.
		}
		if settings.RoutingStrategy == "single_account" && account.ID != settings.SingleAccountID {
			continue
		}
		candidates = append(candidates, account)
	}
	accounts, err = FilterCapabilityAccounts(route, candidates, "")
	if err != nil {
		return nil, err
	}
	var quotaOmissions map[string]bool
	accounts, quotaOmissions, err = p.catalogAccountsWithOmissions(request, accounts, "")
	if err != nil {
		return nil, err
	}
	choices := make([]accountCandidate, 0, len(accounts))
	now := time.Now()
	quotaDefinition, quotaMapped := domain.AdditionalQuotaForModel(model)
	blockedByData, blockedByAdditionalQuota := false, false
	unpriced := 0
	for _, account := range accounts {
		price, err := p.ResolvePrice(ctx, account, model)
		if errors.Is(err, pricing.ErrUnpriced) {
			unpriced++
			continue
		}
		if err != nil {
			return nil, err
		}
		entry := accountCandidate{account: account, price: price}
		if quotaMapped && account.Kind == domain.AccountChatGPT {
			policy := settings.AdditionalQuotaRoutingPolicies[quotaDefinition.QuotaKey]
			if policy == "" {
				policy = quotaDefinition.RoutingPolicy
			}
			if policy != "inherit" {
				entry.account.RoutingPolicy = policy
			}
			additional, err := p.store.ListAccountAdditionalQuotas(ctx, account.ID)
			if err != nil {
				return nil, err
			}
			applies, state, primary, secondary := additionalQuotaEligibility(account, quotaDefinition, additional, quotaOmissions[account.ID], now)
			if state == "data_unavailable" {
				blockedByData = true
				continue
			}
			if state == "quota_exhausted" {
				blockedByAdditionalQuota = true
				continue
			}
			if applies {
				if account.Status == domain.AccountQuotaExceeded {
					refusalAt, err := p.store.LoadAccountQuotaRefusalAt(ctx, account.ID)
					if err != nil {
						return nil, err
					}
					if !additionalQuotaBlockRecovered(refusalAt, primary, secondary, now) {
						continue
					}
				} else if account.Status != domain.AccountActive {
					continue
				}
				if primary != nil {
					entry.primary, entry.primaryReset = quotaSelectionValue(primary.UsedPercent, primary.ResetAt, now)
				}
				if secondary != nil {
					entry.secondary, entry.secondaryReset = quotaSelectionValue(secondary.UsedPercent, secondary.ResetAt, now)
					entry.secondaryKnown = true
				}
				choices = append(choices, entry)
				continue
			}
		}
		credits, err := p.store.LoadAccountCreditStatus(ctx, account.ID)
		if err != nil {
			return nil, err
		}
		quotas, err := p.store.ListAccountQuota(ctx, account.ID)
		if err != nil {
			return nil, err
		}
		var refusalAt *time.Time
		if account.Status == domain.AccountQuotaExceeded {
			refusalAt, err = p.store.LoadAccountQuotaRefusalAt(ctx, account.ID)
			if err != nil {
				return nil, err
			}
		}
		if EffectiveAccountQuotaStatus(account, quotas, credits, refusalAt, now) != domain.AccountActive {
			continue
		}
		primary, long := selectionQuotaWindows(account, quotas)
		if primary != nil {
			entry.primary, entry.primaryReset = quotaSelectionValue(primary.UsedPercent, primary.ResetAt, now)
		}
		if long != nil {
			entry.secondary, entry.secondaryReset = quotaSelectionValue(long.UsedPercent, long.ResetAt, now)
			entry.secondaryKnown = true
		}
		choices = append(choices, entry)
	}
	if len(choices) == 0 {
		if blockedByData {
			return nil, &ProxyError{Code: "additional_quota_data_unavailable", Status: 429, Message: "No fresh additional quota data available for the requested model"}
		}
		if blockedByAdditionalQuota {
			return nil, &ProxyError{Code: "quota_exhausted", Status: 429, Message: "Additional quota exhausted for the requested model"}
		}
		if unpriced > 0 && unpriced == len(accounts) {
			return nil, pricing.ErrUnpriced
		}
		return nil, domain.ErrNoAccounts
	}
	return choices, nil
}

func (p *Proxy) selectAccount(ctx context.Context, keyID string, request *responseRequest, settings domain.RuntimeSettings, route CapabilityRoute, owner *domain.Continuation, previous domain.Account, excluded map[string]bool) (domain.Account, pricing.Price, error) {
	choices, err := p.accountCandidates(ctx, keyID, request, settings, route, owner, previous, excluded)
	if err != nil {
		return domain.Account{}, pricing.Price{}, err
	}
	selected, err := p.chooseAccount(choices, settings)
	return selected.account, selected.price, err
}

func (p *Proxy) selectAccountAdmitted(ctx context.Context, keyID string, request *responseRequest, settings domain.RuntimeSettings, route CapabilityRoute, owner *domain.Continuation, previous domain.Account, excluded map[string]bool, confirmedOwner, streamWork bool, preferredAccountID ...string) (domain.Account, pricing.Price, *accountLease, error) {
	choices, err := p.accountCandidates(ctx, keyID, request, settings, route, owner, previous, excluded)
	if err != nil {
		return domain.Account{}, pricing.Price{}, nil, err
	}
	selected, lease, err := p.admittedAccount(ctx, choices, settings, keyID, confirmedOwner, streamWork, preferredAccountID...)
	return selected.account, selected.price, lease, err
}

func (p *Proxy) retireRequiredSocket(route CapabilityRoute, accountID, keyID string) {
	if !route.RequireSecurityWorkAuthorized {
		return
	}
	if retire, ok := p.provider.(interface{ RetireRequiredCapability(string, string) }); ok {
		retire.RetireRequiredCapability(accountID, keyID)
	}
}

func (p *Proxy) catalogAccounts(request *responseRequest, candidates []domain.Account, ownerID string) ([]domain.Account, error) {
	accounts, _, err := filterCatalogAccounts(p.Catalog, request, candidates, ownerID, false)
	return accounts, err
}

func (p *Proxy) catalogAccountsWithOmissions(request *responseRequest, candidates []domain.Account, ownerID string) ([]domain.Account, map[string]bool, error) {
	return filterCatalogAccounts(p.Catalog, request, candidates, ownerID, true)
}

func (p *Proxy) remember(ctx context.Context, options ResponseOptions, request responseRequest, history responseContext, previous *domain.Continuation, account domain.Account, result ResponseResult, reservationID string, forwardTurn, successful bool) error {
	var response struct {
		Output []json.RawMessage `json:"output"`
	}
	valid := json.Unmarshal(result.Response, &response) == nil && response.Output != nil
	// A same-owner follow-up can still work in the provider's live session after
	// local history was evicted. Its new tail must not become a supposedly full
	// conversation for a later cross-account replay.
	valid = valid && (request.Previous == "" || previous != nil && len(previous.ContextEncrypted) != 0)
	items := append(slices.Clone(history.Items), request.Input...)
	items = append(items, response.Output...)
	owner := domain.Continuation{ResponseID: result.ResponseID, KeyID: options.KeyID, AccountID: account.ID, AccountGeneration: account.Generation, RouteRevision: account.RouteRevision, ProviderID: account.Provider, Model: request.Model, CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(p.config.ContinuationTTL), FilePinned: request.FilePinned || accountBoundItems(items)}
	owner.FilePinned = owner.FilePinned || previous != nil && previous.FilePinned
	if valid {
		plain, err := json.Marshal(responseContext{Items: items})
		if err != nil {
			return err
		}
		if len(plain) <= maxReplayBytes {
			owner.ContextEncrypted, err = p.cipher.Encrypt(plain)
			if err != nil {
				return err
			}
		}
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := p.store.SaveContinuation(cleanup, owner, p.config.ContinuationBounds); err != nil {
		return err
	}
	if !successful {
		return nil // Retain partial response ownership without establishing new aliases.
	}
	return rememberConversationAliases(cleanup, p.store, owner, options.identity(), forwardTurn, reservationID, p.config.ContinuationBounds)
}
