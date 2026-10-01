package application

import (
	"context"
	"io"

	"codex-lb/internal/domain"
)

type OAuthCallback func(context.Context, string) OAuthStatusResult

// Closing the listener must stop admissions without aborting the callback
// currently delivering its result. Network details belong to the adapter.
type OAuthCallbackListener func(OAuthCallback) (io.Closer, error)

func (s *AccountsService) ConfigureCallbacks(listener OAuthCallbackListener) error {
	s.flows.mu.Lock()
	defer s.flows.mu.Unlock()
	if s.flows.closed || len(s.flows.flows) != 0 {
		return domain.ErrConflict
	}
	s.callback = listener
	return nil
}

func (s *AccountsService) Close() error {
	s.flows.mu.Lock()
	s.flows.closed = true
	s.cancel()
	for _, flow := range s.flows.flows {
		if flow.pollCancel != nil {
			flow.pollCancel()
		}
		if flow.expiryTimer != nil {
			flow.expiryTimer.Stop()
		}
	}
	s.stopCallbackServerLocked()
	s.flows.mu.Unlock()
	s.flows.jobs.Wait()
	return nil
}
