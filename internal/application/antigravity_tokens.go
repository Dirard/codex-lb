package application

import (
	"context"
	"errors"
	"time"

	"codex-lb/internal/domain"
)

const (
	antigravityRefreshLead    = 5 * time.Minute
	antigravityRefreshTimeout = 30 * time.Second
	antigravityWriteTimeout   = 10 * time.Second
	antigravityTokenAttempts  = 3
)

// AntigravityAccessToken realizes the workflow step "take a fresh access
// token" for a request routed to an Antigravity source. Tokens live in memory
// with their expiry and a version; one refresh runs at a time per source, 5
// minutes before expiry or when rejectedVersion names the token Google just
// rejected. A refresh token Google rotates is stored; a refused one marks the
// source's account reauth_required. The project is the source's own.
func (s *AntigravityService) AntigravityAccessToken(ctx context.Context, sourceID string, rejectedVersion int64) (string, int64, string, error) {
	source, err := s.store.GetModelSource(ctx, sourceID)
	if errors.Is(err, domain.ErrNotFound) {
		return "", 0, "", ErrAntigravityAccountUnavailable
	}
	if err != nil {
		return "", 0, "", err
	}
	info := source.ProviderConfig.Antigravity
	if source.Kind != domain.ModelSourceAntigravity || info == nil || info.Project == "" ||
		source.AccountStatus == domain.AccountReauthRequired || source.AccountStatus == domain.AccountDeactivated {
		return "", 0, "", ErrAntigravityAccountUnavailable
	}
	for attempt := 0; attempt < antigravityTokenAttempts; attempt++ {
		s.mu.Lock()
		cached, ok := s.tokens[sourceID]
		if ok && cached.version != rejectedVersion && cached.expires.After(s.now().Add(antigravityRefreshLead)) {
			s.mu.Unlock()
			return cached.access, cached.version, info.Project, nil
		}
		flight, joined := s.flights[sourceID]
		if !joined {
			flight = &antigravityFlight{done: make(chan struct{})}
			s.flights[sourceID] = flight
		}
		s.mu.Unlock()
		if !joined {
			// The leader's refresh outlives its caller: a rotated refresh
			// token must reach the database even if the client went away.
			refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), antigravityRefreshTimeout)
			flight.err = s.refreshToken(refreshCtx, sourceID)
			cancel()
			s.mu.Lock()
			delete(s.flights, sourceID)
			s.mu.Unlock()
			close(flight.done)
		} else {
			select {
			case <-flight.done:
			case <-ctx.Done():
				return "", 0, "", ctx.Err()
			}
		}
		if flight.err != nil {
			return "", 0, "", flight.err
		}
		rejectedVersion = -1 // A finished flight produced a newer token.
	}
	return "", 0, "", errors.New("antigravity token stays stale after a refresh")
}

// refreshToken performs one refresh with the stored refresh token.
func (s *AntigravityService) refreshToken(ctx context.Context, sourceID string) error {
	credential, err := s.store.GetAccountCredential(ctx, sourceID)
	if errors.Is(err, domain.ErrNotFound) {
		return ErrAntigravityAccountUnavailable
	}
	if err != nil {
		return err
	}
	if len(credential.ExternalKeyEncrypted) == 0 {
		return ErrAntigravityAccountUnavailable
	}
	plain, err := s.cipher.Decrypt(credential.ExternalKeyEncrypted)
	if err != nil {
		return errors.New("could not decrypt the Antigravity credential")
	}
	tokens, err := s.upstream.Refresh(ctx, string(plain))
	if errors.Is(err, ErrAntigravityGrantRefused) {
		s.forget(sourceID)
		if markErr := s.store.SetAntigravityAccountStatus(ctx, sourceID, domain.AccountReauthRequired, "", nil); markErr != nil && !errors.Is(markErr, domain.ErrNotFound) {
			return markErr
		}
		s.log.Warn("antigravity refresh token refused; the account needs a new sign-in", "source_id", sourceID)
		return ErrAntigravityAccountUnavailable
	}
	if err != nil {
		return err
	}
	if tokens.Refresh != "" && tokens.Refresh != string(plain) {
		rotated, err := s.cipher.Encrypt([]byte(tokens.Refresh))
		if err != nil {
			return errors.New("could not encrypt the Antigravity credential")
		}
		// A newer sign-in wins over a rotation of the token it replaced; the
		// access token is for the same account either way.
		if _, err := s.store.RotateExternalCredential(ctx, sourceID, credential.ExternalKeyEncrypted, rotated); err != nil {
			return err
		}
	}
	s.remember(sourceID, tokens)
	return nil
}

// remember caches a fresh access token under a new version.
func (s *AntigravityService) remember(sourceID string, tokens AntigravityTokenSet) {
	expiresIn := tokens.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = time.Hour
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.version++
	s.tokens[sourceID] = antigravityAccessToken{access: tokens.Access, expires: s.now().Add(expiresIn), version: s.version}
}

func (s *AntigravityService) forget(sourceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tokens, sourceID)
}

// ReportAntigravityRefusal realizes the workflow step "apply the account
// effect" of a classified Cloud Code refusal: a suspension (account_suspended)
// deactivates the source's account with reason tos_violation and keeps the
// appeal link; a verification request (account_validation_required) puts it
// in reauth_required with the verification link. Other codes change nothing.
func (s *AntigravityService) ReportAntigravityRefusal(ctx context.Context, sourceID, code, actionURL string) error {
	var status domain.AccountStatus
	var reason string
	switch code {
	case "account_suspended":
		status, reason = domain.AccountDeactivated, "tos_violation"
	case "account_validation_required":
		status = domain.AccountReauthRequired
	default:
		return nil
	}
	link := safeActionURL(actionURL)
	s.forget(sourceID)
	// The effect is recorded even when the refused request is already over.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), antigravityWriteTimeout)
	defer cancel()
	if err := s.store.SetAntigravityAccountStatus(writeCtx, sourceID, status, reason, &link); err != nil {
		return err
	}
	s.log.Warn("antigravity account refused by Google", "source_id", sourceID, "code", code)
	return nil
}

// RefreshModels lists the account's models again: known models keep their
// prices and enabled flags, new ones are added at price zero and models
// Google no longer lists are disabled.
func (s *AntigravityService) RefreshModels(ctx context.Context, sourceID string) (domain.ModelSource, error) {
	source, err := s.store.GetModelSource(ctx, sourceID)
	if errors.Is(err, domain.ErrNotFound) || err == nil && source.Kind != domain.ModelSourceAntigravity {
		return domain.ModelSource{}, antigravitySourceNotFound()
	}
	if err != nil {
		return domain.ModelSource{}, err
	}
	rejected := int64(-1)
	var listed []AntigravityModel
	for attempt := 0; ; attempt++ {
		token, version, project, err := s.AntigravityAccessToken(ctx, sourceID, rejected)
		if errors.Is(err, ErrAntigravityAccountUnavailable) {
			return domain.ModelSource{}, &ProxyError{Code: "antigravity_reauth_required", Status: 409,
				Message: "This Antigravity account needs a new sign-in before its models can be listed"}
		}
		if err != nil {
			if ctx.Err() != nil {
				return domain.ModelSource{}, ctx.Err()
			}
			return domain.ModelSource{}, antigravityUnavailable("Google did not renew this account's access; try again later")
		}
		listed, err = s.upstream.AvailableModels(ctx, token, project)
		if errors.Is(err, ErrAntigravityUnauthorized) && attempt == 0 {
			rejected = version
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return domain.ModelSource{}, ctx.Err()
			}
			return domain.ModelSource{}, antigravityModelsUnavailable()
		}
		break
	}
	// The listing took a network round trip; merge into the current row.
	source, err = s.store.GetModelSource(ctx, sourceID)
	if err != nil {
		return domain.ModelSource{}, err
	}
	now := s.now().UTC()
	source.Models = mergeAntigravityModels(source.Models, listed, now)
	source.UpdatedAt = now
	if err := s.store.SaveModelSource(ctx, source, nil); err != nil {
		return domain.ModelSource{}, err
	}
	return s.store.GetModelSource(ctx, sourceID)
}
