package application

import (
	"context"
	"errors"
	"sync"
	"time"

	"codex-lb/internal/domain"
)

type TokenRepository interface {
	GetAccountCredential(context.Context, string) (domain.AccountCredential, error)
	RotateAccountCredential(context.Context, domain.AccountCredential, domain.AccountCredential, time.Time) (bool, error)
	MarkAccountReauthRequired(context.Context, domain.AccountCredential, string) error
}

type OAuthRefresher interface {
	Refresh(context.Context, string) (OAuthTokens, error)
}

type tokenRefresh struct {
	done  chan struct{}
	token string
	err   error
}

type accountGenerationKey struct {
	accountID  string
	generation int64
}

type TokenService struct {
	store    TokenRepository
	cipher   SecretCipher
	refresh  OAuthRefresher
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	jobs     map[accountGenerationKey]*tokenRefresh
	capacity chan struct{}
	wg       sync.WaitGroup
	closed   bool
}

func NewTokenService(store TokenRepository, cipher SecretCipher, refresh OAuthRefresher) *TokenService {
	ctx, cancel := context.WithCancel(context.Background())
	return &TokenService{store: store, cipher: cipher, refresh: refresh, ctx: ctx, cancel: cancel, jobs: make(map[accountGenerationKey]*tokenRefresh), capacity: make(chan struct{}, 4)}
}

func (s *TokenService) AccessToken(ctx context.Context, account domain.Account, credential domain.AccountCredential) (string, error) {
	if account.ID != credential.AccountID || account.Generation != credential.Generation {
		return "", domain.ErrInvalid
	}
	plain, err := s.cipher.Decrypt(credential.AccessTokenEncrypted)
	if err != nil {
		return "", err
	}
	if tokenFresh(string(plain), account.LastRefresh, time.Now()) {
		return string(plain), nil
	}
	return s.renew(ctx, account.ID, account.Generation, false, "")
}

func tokenFresh(token string, lastRefresh *time.Time, now time.Time) bool {
	if token == "" {
		return false
	}
	expires := parseIDTokenClaims(token).expiryEpochMillis
	if expires > 0 {
		return time.UnixMilli(expires).After(now.Add(30 * time.Second))
	}
	return lastRefresh != nil && lastRefresh.Add(8*24*time.Hour).After(now)
}

// ForceRefresh is only for a definitive upstream authentication rejection,
// before output, not a retry mechanism for quota, timeout or partial responses.
func (s *TokenService) ForceRefresh(ctx context.Context, account domain.Account, rejectedToken string) (string, error) {
	if rejectedToken == "" {
		return "", domain.ErrInvalid
	}
	return s.renew(ctx, account.ID, account.Generation, true, rejectedToken)
}

func (s *TokenService) renew(ctx context.Context, accountID string, generation int64, force bool, rejectedToken string) (string, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return "", context.Canceled
	}
	key := accountGenerationKey{accountID: accountID, generation: generation}
	job := s.jobs[key]
	if job == nil {
		job = &tokenRefresh{done: make(chan struct{})}
		s.jobs[key] = job
		s.wg.Add(1)
		go s.run(key, job, force, rejectedToken)
	}
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-job.done:
		return job.token, job.err
	}
}

func (s *TokenService) run(key accountGenerationKey, job *tokenRefresh, force bool, rejectedToken string) {
	defer s.wg.Done()
	defer func() {
		s.mu.Lock()
		delete(s.jobs, key)
		close(job.done)
		s.mu.Unlock()
	}()
	// One cancelled request must not abandon a rotating refresh token that other
	// waiters need. The bounded job belongs to this service, not to its first caller.
	ctx, cancel := context.WithTimeout(s.ctx, 8*time.Second)
	defer cancel()
	select {
	case s.capacity <- struct{}{}:
		defer func() { <-s.capacity }()
	case <-ctx.Done():
		job.err = ctx.Err()
		return
	}
	job.token, job.err = s.exchange(ctx, key, force, rejectedToken)
}

func (s *TokenService) exchange(ctx context.Context, key accountGenerationKey, force bool, rejectedToken string) (string, error) {
	expected, err := s.store.GetAccountCredential(ctx, key.accountID)
	if err != nil {
		return "", err
	}
	if expected.Generation != key.generation {
		return "", domain.ErrConflict
	}
	access, err := s.cipher.Decrypt(expected.AccessTokenEncrypted)
	if err != nil {
		return "", err
	}
	// A delayed 401 for an old access token must not rotate the newly persisted
	// refresh token again after another requester already completed that work.
	if force && string(access) != rejectedToken && len(access) != 0 {
		return string(access), nil
	}
	if !force && tokenFresh(string(access), nil, time.Now()) {
		return string(access), nil
	}
	refresh, err := s.cipher.Decrypt(expected.RefreshTokenEncrypted)
	if err != nil {
		return "", err
	}
	tokens, err := s.refresh.Refresh(ctx, string(refresh))
	if err != nil {
		var oauth *OAuthError
		if errors.As(err, &oauth) && permanentRefreshError(oauth.Code) {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if markErr := s.store.MarkAccountReauthRequired(cleanup, expected, oauth.Code); markErr != nil {
				return "", markErr
			}
		}
		return "", err
	}
	if tokens.AccessToken == "" {
		return "", errors.New("OAuth refresh omitted access token")
	}
	next := expected
	next.AccessTokenEncrypted, err = s.cipher.Encrypt([]byte(tokens.AccessToken))
	if err != nil {
		return "", err
	}
	if tokens.RefreshToken != "" {
		next.RefreshTokenEncrypted, err = s.cipher.Encrypt([]byte(tokens.RefreshToken))
		if err != nil {
			return "", err
		}
	}
	if tokens.IDToken != "" {
		next.IDTokenEncrypted, err = s.cipher.Encrypt([]byte(tokens.IDToken))
		if err != nil {
			return "", err
		}
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	changed, err := s.store.RotateAccountCredential(cleanup, expected, next, time.Now().UTC())
	if err != nil {
		return "", err
	}
	if !changed {
		// A concurrent explicit login/import wins over this older refresh.
		current, err := s.store.GetAccountCredential(cleanup, key.accountID)
		if err != nil {
			return "", err
		}
		if current.Generation != key.generation {
			return "", domain.ErrConflict
		}
		plain, err := s.cipher.Decrypt(current.AccessTokenEncrypted)
		return string(plain), err
	}
	return tokens.AccessToken, nil
}

func permanentRefreshError(code string) bool {
	switch code {
	case "invalid_grant", "invalid_refresh_token", "refresh_token_expired", "refresh_token_reused", "refresh_token_invalidated", "token_expired", "account_deactivated":
		return true
	default:
		return false
	}
}

func (s *TokenService) Close() error {
	s.mu.Lock()
	s.closed = true
	s.cancel()
	s.mu.Unlock()
	s.wg.Wait()
	return nil
}
