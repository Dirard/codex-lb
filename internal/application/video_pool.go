package application

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"codex-lb/internal/domain"
)

const (
	videoRefreshMargin  = time.Minute
	videoRefreshClaim   = 30 * time.Second
	videoRefreshWaits   = 10
	videoTokenAttempts  = 3
	videoRefreshWait    = 500 * time.Millisecond
	videoWriteTimeout   = 10 * time.Second
	videoRefreshTimeout = 30 * time.Second
	videoRevokeTimeout  = 10 * time.Second
	maxVideoLabelBytes  = 128
)

// ErrVideoRefreshRefused means the issuer definitively refused a refresh token.
var ErrVideoRefreshRefused = errors.New("video account refresh refused")

// VideoTokens are plaintext tokens on their way to the cipher.
type VideoTokens struct {
	Access    string
	Refresh   string
	ExpiresIn time.Duration
}

// VideoUpstreamResponse is one bounded answer of the xAI video API.
type VideoUpstreamResponse struct {
	Status     int
	Body       []byte
	RetryAfter time.Duration
}

// VideoUpstream is the xAI port of the video pool.
type VideoUpstream interface {
	Refresh(ctx context.Context, clientID, refreshToken string) (VideoTokens, error)
	Revoke(ctx context.Context, clientID, refreshToken string) error
	StartDeviceLogin(ctx context.Context) (VideoDeviceLogin, error)
	PollDeviceLogin(ctx context.Context, clientID, deviceCode string) (VideoTokens, error)
	Identity(ctx context.Context, accessToken string) (VideoIdentity, error)
	Usage(ctx context.Context, accessToken string) (VideoUsage, error)
	CreateVideo(ctx context.Context, token string, body []byte) (VideoUpstreamResponse, error)
	GetVideo(ctx context.Context, token, requestID string) (VideoUpstreamResponse, error)
}

// VideoStore persists the pool. It shares nothing with account routing.
type VideoStore interface {
	SaveVideoAccount(context.Context, domain.VideoAccount) (domain.VideoAccount, error)
	GetVideoAccount(context.Context, string) (domain.VideoAccount, error)
	ListVideoAccounts(context.Context) ([]domain.VideoAccount, map[string]domain.VideoAccountStats, error)
	SetVideoAccountLabel(ctx context.Context, id, label string, now time.Time) error
	SetVideoAccountStatus(ctx context.Context, id string, status domain.VideoAccountStatus, code string, now time.Time) error
	SetVideoCooldown(ctx context.Context, id, code string, until, now time.Time) error
	DeleteVideoAccount(ctx context.Context, id string, now time.Time) error
	ClaimVideoRefresh(ctx context.Context, id string, version int64, until, now time.Time) (bool, error)
	SaveVideoTokens(ctx context.Context, id string, version int64, access, refresh []byte, expires, now time.Time) (bool, error)
	SaveVideoUsage(ctx context.Context, id string, percent float64, periodEnd, now time.Time) error
	SetVideoUsageError(ctx context.Context, id, code string, now time.Time) error
	IsVideoKeyAllowed(context.Context, string) (bool, error)
	ListVideoKeys(context.Context) ([]domain.VideoKeyAccess, error)
	AllowVideoKey(ctx context.Context, keyID string, now time.Time) error
	DenyVideoKey(ctx context.Context, keyID string) error
	ReserveVideoJob(ctx context.Context, job domain.VideoJob, excluded map[string]bool, now time.Time) (domain.VideoAccount, error)
	NearestVideoCooldown(ctx context.Context, now time.Time) (*time.Time, error)
	MarkVideoJobPending(ctx context.Context, id, upstreamRequestID string, nextPoll time.Time, answer domain.VideoUpstreamAnswer) error
	FinishVideoJob(ctx context.Context, id string, finish domain.VideoJobFinish) (bool, error)
	ScheduleVideoPoll(ctx context.Context, id string, next time.Time, answer domain.VideoUpstreamAnswer) error
	GetVideoJob(context.Context, string) (domain.VideoJob, error)
	ListVideoJobs(ctx context.Context, keyID string, before *domain.VideoJobCursor, limit int) ([]domain.VideoJob, error)
	DueVideoJobs(ctx context.Context, now time.Time, limit int) ([]domain.VideoJob, error)
	ExpireStaleVideoJobs(ctx context.Context, pendingBefore, dispatchingBefore, now time.Time) (int, error)
	PruneVideoJobs(ctx context.Context, before time.Time, limit int) (int, error)
	VideoJobEntries(ctx context.Context, before *domain.VideoJobCursor, limit int) ([]domain.VideoJobEntry, error)
	VideoTotalsSince(ctx context.Context, since time.Time) (domain.VideoPeriodTotals, error)
}

// VideoService is the separate "video generation" function: its own account
// pool, key allow list, jobs and accounting behind the shared API key.
type VideoService struct {
	store     VideoStore
	cipher    SecretCipher
	upstream  VideoUpstream
	admission CodexAdmission
	now       func() time.Time
	wait      func(context.Context, time.Duration) error
	log       *slog.Logger

	mu      sync.Mutex
	flights map[string]*videoRefreshFlight
	logins  map[string]*videoLoginFlow
}

type videoRefreshFlight struct {
	done chan struct{}
	err  error
}

func NewVideoService(store VideoStore, cipher SecretCipher, upstream VideoUpstream) *VideoService {
	return &VideoService{store: store, cipher: cipher, upstream: upstream, admission: blockedCodexAdmission{}, now: time.Now,
		wait: waitVideo, log: slog.New(slog.DiscardHandler), flights: map[string]*videoRefreshFlight{}, logins: map[string]*videoLoginFlow{}}
}

// ConfigureClock replaces the clock; tests use it to reach poll and stale deadlines.
func (s *VideoService) ConfigureClock(now func() time.Time) { s.now = now }

// ConfigureLogger sets where unfinished jobs leave their trace.
func (s *VideoService) ConfigureLogger(logger *slog.Logger) { s.log = logger }

// ConfigureAdmission shares the proxy's bounded admission and drain state.
func (s *VideoService) ConfigureAdmission(admission CodexAdmission) { s.admission = admission }

func waitVideo(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func videoID(prefix string) string { return prefix + strings.ToLower(rand.Text()) }

func videoWriteContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), videoWriteTimeout)
}

// VideoAccountView joins an account with its totals.
type VideoAccountView struct {
	domain.VideoAccount
	domain.VideoAccountStats
	CostUSD float64 `json:"costUsd"`
}

func (s *VideoService) ListAccounts(ctx context.Context) ([]VideoAccountView, error) {
	accounts, stats, err := s.store.ListVideoAccounts(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now()
	views := make([]VideoAccountView, 0, len(accounts))
	for _, account := range accounts {
		if account.CooldownUntil != nil && !account.CooldownUntil.After(now) {
			account.CooldownUntil = nil
		}
		entry := stats[account.ID]
		views = append(views, VideoAccountView{VideoAccount: account, VideoAccountStats: entry, CostUSD: float64(entry.CostMicrodollars) / 1e6})
	}
	return views, nil
}

// UpdateAccount renames, pauses or resumes. An account that lost its session
// comes back only through a new sign-in.
func (s *VideoService) UpdateAccount(ctx context.Context, id string, label *string, status *domain.VideoAccountStatus) error {
	account, err := s.store.GetVideoAccount(ctx, id)
	if err != nil {
		return err
	}
	now := s.now().UTC()
	if label != nil {
		trimmed := strings.TrimSpace(*label)
		if len(trimmed) > maxVideoLabelBytes {
			return &ProxyError{Code: "invalid_request", Status: 400, Message: "The label is too long", Param: "label"}
		}
		if err := s.store.SetVideoAccountLabel(ctx, id, trimmed, now); err != nil {
			return err
		}
	}
	if status == nil {
		return nil
	}
	switch *status {
	case domain.VideoAccountPaused, domain.VideoAccountActive:
	default:
		return &ProxyError{Code: "invalid_request", Status: 400, Message: "The status must be active or paused", Param: "status"}
	}
	if *status == domain.VideoAccountActive && account.Status == domain.VideoAccountReauthRequired {
		return &ProxyError{Code: "video_reauth_required", Status: 409, Message: "This account needs a new sign-in"}
	}
	return s.store.SetVideoAccountStatus(ctx, id, *status, "", now)
}

// DeleteAccount revokes the session when it can and removes the account. A
// failed revoke does not keep an account the operator wants gone.
func (s *VideoService) DeleteAccount(ctx context.Context, id string) error {
	account, err := s.store.GetVideoAccount(ctx, id)
	if err != nil {
		return err
	}
	if refresh, err := s.cipher.Decrypt(account.RefreshTokenEncrypted); err == nil {
		revokeCtx, cancel := context.WithTimeout(ctx, videoRevokeTimeout)
		_ = s.upstream.Revoke(revokeCtx, account.OIDCClientID, string(refresh))
		cancel()
	}
	writeCtx, done := videoWriteContext(ctx)
	defer done()
	return s.store.DeleteVideoAccount(writeCtx, id, s.now().UTC())
}

func (s *VideoService) ListKeys(ctx context.Context) ([]domain.VideoKeyAccess, error) {
	return s.store.ListVideoKeys(ctx)
}

func (s *VideoService) AllowKey(ctx context.Context, keyID string) error {
	return s.store.AllowVideoKey(ctx, keyID, s.now().UTC())
}

func (s *VideoService) DenyKey(ctx context.Context, keyID string) error {
	return s.store.DenyVideoKey(ctx, keyID)
}

// KeyAllowed is the access check of the video REST surface; it runs before the
// body is read. Internal principals are never allowed.
func (s *VideoService) KeyAllowed(ctx context.Context, keyID string) error {
	if !domain.IsInternalKey(keyID) {
		allowed, err := s.store.IsVideoKeyAllowed(ctx, keyID)
		if err != nil {
			return err
		}
		if allowed {
			return nil
		}
	}
	return &ProxyError{Code: "video_access_denied", Status: 403, Message: "This API key is not allowed to use video generation"}
}

// accessToken returns a usable token for an account. usedVersion, when not
// negative, asks for a token newer than the one the upstream just rejected.
func (s *VideoService) accessToken(ctx context.Context, accountID string, usedVersion int64) (string, int64, error) {
	for attempt := 0; attempt < videoTokenAttempts; attempt++ {
		account, err := s.store.GetVideoAccount(ctx, accountID)
		if err != nil {
			return "", 0, err
		}
		if account.Status == domain.VideoAccountReauthRequired {
			return "", 0, ErrVideoRefreshRefused
		}
		stale := account.TokenVersion == usedVersion || !account.AccessExpiresAt.After(s.now().Add(videoRefreshMargin))
		if !stale {
			plain, err := s.cipher.Decrypt(account.AccessTokenEncrypted)
			return string(plain), account.TokenVersion, err
		}
		s.mu.Lock()
		flight, joined := s.flights[accountID]
		if !joined {
			flight = &videoRefreshFlight{done: make(chan struct{})}
			s.flights[accountID] = flight
		}
		s.mu.Unlock()
		if !joined {
			// The leader's refresh outlives its caller: a rotated refresh
			// token must reach the database even if the client went away.
			refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), videoRefreshTimeout)
			flight.err = s.refreshAccount(refreshCtx, account)
			cancel()
			s.mu.Lock()
			delete(s.flights, accountID)
			s.mu.Unlock()
			close(flight.done)
		} else {
			select {
			case <-flight.done:
			case <-ctx.Done():
				return "", 0, ctx.Err()
			}
		}
		if flight.err != nil {
			return "", 0, flight.err
		}
		usedVersion = -1 // A finished flight produced or found newer tokens.
	}
	return "", 0, errors.New("video account token stays stale after a refresh")
}

// refreshAccount performs one guarded refresh: no transaction spans the call,
// a claim keeps another worker from rotating the same refresh token, and a
// version check keeps a stale result from overwriting newer tokens.
func (s *VideoService) refreshAccount(ctx context.Context, account domain.VideoAccount) error {
	claimed := false
	for attempt := 0; attempt < videoRefreshWaits; attempt++ {
		now := s.now().UTC()
		var err error
		claimed, err = s.store.ClaimVideoRefresh(ctx, account.ID, account.TokenVersion, now.Add(videoRefreshClaim), now)
		if err != nil {
			return err
		}
		if claimed {
			break
		}
		if err := s.wait(ctx, videoRefreshWait); err != nil {
			return err
		}
		current, err := s.store.GetVideoAccount(ctx, account.ID)
		if err != nil {
			return err
		}
		if current.TokenVersion != account.TokenVersion {
			return nil // Another worker refreshed it.
		}
	}
	if !claimed {
		return errors.New("video account refresh is busy")
	}
	plain, err := s.cipher.Decrypt(account.RefreshTokenEncrypted)
	if err != nil {
		return err
	}
	tokens, err := s.upstream.Refresh(ctx, account.OIDCClientID, string(plain))
	if errors.Is(err, ErrVideoRefreshRefused) {
		if markErr := s.store.SetVideoAccountStatus(ctx, account.ID, domain.VideoAccountReauthRequired, "refresh_refused", s.now().UTC()); markErr != nil {
			return markErr
		}
		return err
	}
	if err != nil {
		return err
	}
	access, err := s.cipher.Encrypt([]byte(tokens.Access))
	if err != nil {
		return err
	}
	refresh, err := s.cipher.Encrypt([]byte(tokens.Refresh))
	if err != nil {
		return err
	}
	now := s.now().UTC()
	_, err = s.store.SaveVideoTokens(ctx, account.ID, account.TokenVersion, access, refresh, now.Add(tokens.ExpiresIn), now)
	return err
}
