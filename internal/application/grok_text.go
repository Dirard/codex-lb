package application

import (
	"context"
	"errors"

	"codex-lb/internal/domain"
)

var (
	// ErrGrokUnauthorized is xAI rejecting a Grok account's access token.
	ErrGrokUnauthorized = errors.New("grok token was rejected")
	// ErrGrokAccountUnavailable is a Grok account that cannot lend a token:
	// it is gone, needs a new sign-in or was denied access.
	ErrGrokAccountUnavailable = errors.New("grok account is unavailable")
)

// GrokTextModel is one text model a Grok subscription serves, with xAI's list
// prices in USD per 1M tokens; nil means xAI gave no price.
type GrokTextModel struct {
	ID               string
	Aliases          []string
	Vision           bool
	InputPerMillion  *float64
	CachedPerMillion *float64
	OutputPerMillion *float64
}

// GrokAccessToken takes the account's current token for a text request
// routed to a Grok subscription source. rejectedVersion, when not negative,
// asks for a token newer than the one xAI just rejected. Pausing the account
// for video does not stop text; a lost session or a denial does.
func (s *VideoService) GrokAccessToken(ctx context.Context, accountID string, rejectedVersion int64) (string, int64, error) {
	if err := s.grokAccountUsable(ctx, accountID); err != nil {
		return "", 0, err
	}
	token, version, err := s.accessToken(ctx, accountID, rejectedVersion)
	if errors.Is(err, ErrVideoRefreshRefused) {
		return "", 0, ErrGrokAccountUnavailable
	}
	return token, version, err
}

// GrokTextModels fills a new Grok subscription source with the text models
// xAI lists for the account, renewing a rejected token once.
func (s *VideoService) GrokTextModels(ctx context.Context, accountID string) ([]GrokTextModel, error) {
	rejected := int64(-1)
	for attempt := 0; ; attempt++ {
		token, version, err := s.GrokAccessToken(ctx, accountID, rejected)
		if err != nil {
			return nil, err
		}
		models, err := s.upstream.LanguageModels(ctx, token)
		if !errors.Is(err, ErrGrokUnauthorized) || attempt > 0 {
			return models, err
		}
		rejected = version
	}
}

// GrokAccountExists tells model source creation whether the named account is
// in the pool, whatever its status.
func (s *VideoService) GrokAccountExists(ctx context.Context, accountID string) (bool, error) {
	_, err := s.store.GetVideoAccount(ctx, accountID)
	if errors.Is(err, domain.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (s *VideoService) grokAccountUsable(ctx context.Context, accountID string) error {
	account, err := s.store.GetVideoAccount(ctx, accountID)
	if errors.Is(err, domain.ErrNotFound) {
		return ErrGrokAccountUnavailable
	}
	if err != nil {
		return err
	}
	switch account.Status {
	case domain.VideoAccountActive, domain.VideoAccountPaused:
		return nil
	default:
		return ErrGrokAccountUnavailable
	}
}
