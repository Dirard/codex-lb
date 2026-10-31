package application

import (
	"context"
	"errors"
	"sync"
	"time"

	"codex-lb/internal/domain"
)

const (
	maxVideoLogins      = 8
	videoLoginRetention = 10 * time.Minute
	videoLoginSlowDown  = 5 * time.Second
	videoLoginTimeout   = 30 * time.Second
)

// Answers of the device-code exchange that are not tokens.
var (
	ErrVideoLoginPending  = errors.New("video login is waiting for approval")
	ErrVideoLoginSlowDown = errors.New("video login polls too fast")
	ErrVideoLoginRefused  = errors.New("video login was refused or expired")
)

// VideoDeviceLogin is the upstream's answer to a device authorization request.
type VideoDeviceLogin struct {
	ClientID        string
	DeviceCode      string
	UserCode        string
	VerificationURL string
	Interval        time.Duration
	ExpiresIn       time.Duration
}

// VideoIdentity says who signed in.
type VideoIdentity struct {
	UserID string
	Email  string
}

// VideoLoginStart is what the dashboard shows the administrator.
type VideoLoginStart struct {
	FlowID          string `json:"flowId"`
	UserCode        string `json:"userCode"`
	VerificationURL string `json:"verificationUrl"`
	IntervalSeconds int    `json:"intervalSeconds"`
	ExpiresInSecond int    `json:"expiresInSeconds"`
}

// VideoLoginStatus is the state of one sign-in.
type VideoLoginStatus struct {
	Status  string `json:"status"` // pending, success or error
	Email   string `json:"email,omitempty"`
	Message string `json:"message,omitempty"`
}

// videoLoginFlow lives in memory for one sign-in. The device code never reaches
// the database or the browser. Status requests drive it, so no goroutine
// outlives a request and an abandoned flow simply expires.
type videoLoginFlow struct {
	mu         sync.Mutex
	clientID   string
	deviceCode string
	interval   time.Duration
	nextPoll   time.Time
	expiresAt  time.Time
	polling    bool
	status     VideoLoginStatus
	finishedAt time.Time
}

// StartLogin begins a sign-in the administrator approves in their browser.
func (s *VideoService) StartLogin(ctx context.Context) (VideoLoginStart, error) {
	now := s.now()
	s.mu.Lock()
	active := s.pruneLoginsLocked(now)
	s.mu.Unlock()
	if active >= maxVideoLogins {
		return VideoLoginStart{}, &ProxyError{Code: "video_login_limit", Status: 429, Message: "Too many sign-ins are in progress; finish or cancel one"}
	}
	login, err := s.upstream.StartDeviceLogin(ctx)
	if err != nil {
		return VideoLoginStart{}, &ProxyError{Code: "xai_unavailable", Status: 502, Message: "xAI could not start a sign-in"}
	}
	id := videoID("vlogin_")
	flow := &videoLoginFlow{clientID: login.ClientID, deviceCode: login.DeviceCode, interval: login.Interval, nextPoll: now,
		expiresAt: now.Add(login.ExpiresIn), status: VideoLoginStatus{Status: "pending"}}
	s.mu.Lock()
	s.logins[id] = flow
	s.mu.Unlock()
	return VideoLoginStart{FlowID: id, UserCode: login.UserCode, VerificationURL: login.VerificationURL,
		IntervalSeconds: int(login.Interval.Seconds()), ExpiresInSecond: int(login.ExpiresIn.Seconds())}, nil
}

// pruneLoginsLocked forgets old flows and returns how many are still pending.
func (s *VideoService) pruneLoginsLocked(now time.Time) int {
	active := 0
	for id, flow := range s.logins {
		flow.mu.Lock()
		finished := flow.status.Status != "pending"
		stale := finished && now.Sub(flow.finishedAt) > videoLoginRetention || !finished && !now.Before(flow.expiresAt.Add(videoLoginRetention))
		if !finished && now.Before(flow.expiresAt) {
			active++
		}
		flow.mu.Unlock()
		if stale {
			delete(s.logins, id)
		}
	}
	return active
}

// LoginStatus reports a sign-in and, when due, asks xAI about it once.
func (s *VideoService) LoginStatus(ctx context.Context, id string) (VideoLoginStatus, error) {
	s.mu.Lock()
	flow := s.logins[id]
	s.mu.Unlock()
	if flow == nil {
		return VideoLoginStatus{}, &ProxyError{Code: "video_login_not_found", Status: 404, Message: "This sign-in is over; start a new one"}
	}
	now := s.now()
	flow.mu.Lock()
	if flow.status.Status == "pending" && !now.Before(flow.expiresAt) {
		flow.finishLocked(VideoLoginStatus{Status: "error", Message: "The sign-in expired; start a new one"}, now)
	}
	if flow.status.Status != "pending" || flow.polling || now.Before(flow.nextPoll) {
		status := flow.status
		flow.mu.Unlock()
		return status, nil
	}
	flow.polling = true
	clientID, deviceCode := flow.clientID, flow.deviceCode
	flow.mu.Unlock()

	// An approved device code works once, so its exchange and save outlive a
	// status request whose browser went away.
	pollCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), videoLoginTimeout)
	defer cancel()
	result, wait := s.exchangeLogin(pollCtx, clientID, deviceCode)
	now = s.now()
	flow.mu.Lock()
	defer flow.mu.Unlock()
	flow.polling = false
	if result.Status != "pending" {
		flow.finishLocked(result, now)
	} else {
		flow.interval += wait
		flow.nextPoll = now.Add(flow.interval)
	}
	return flow.status, nil
}

func (f *videoLoginFlow) finishLocked(status VideoLoginStatus, now time.Time) {
	f.status, f.finishedAt, f.deviceCode = status, now, ""
}

// exchangeLogin runs one exchange. It returns extra poll delay on slow_down.
func (s *VideoService) exchangeLogin(ctx context.Context, clientID, deviceCode string) (VideoLoginStatus, time.Duration) {
	tokens, err := s.upstream.PollDeviceLogin(ctx, clientID, deviceCode)
	switch {
	case errors.Is(err, ErrVideoLoginSlowDown):
		return VideoLoginStatus{Status: "pending"}, videoLoginSlowDown
	case errors.Is(err, ErrVideoLoginRefused):
		return VideoLoginStatus{Status: "error", Message: "xAI refused or expired this sign-in; start a new one"}, 0
	case err != nil:
		return VideoLoginStatus{Status: "pending"}, 0 // Waiting for approval, or a temporary failure.
	}
	identity, err := s.upstream.Identity(ctx, tokens.Access)
	if err != nil {
		s.revokeUnsaved(ctx, clientID, tokens.Refresh)
		return VideoLoginStatus{Status: "error", Message: "xAI did not say which account signed in; start a new sign-in"}, 0
	}
	access, err := s.cipher.Encrypt([]byte(tokens.Access))
	if err == nil {
		var refresh []byte
		if refresh, err = s.cipher.Encrypt([]byte(tokens.Refresh)); err == nil {
			now := s.now().UTC()
			_, err = s.store.SaveVideoAccount(ctx, domain.VideoAccount{ID: videoID("vacc_"), XAIUserID: identity.UserID, Email: identity.Email,
				OIDCClientID: clientID, AccessTokenEncrypted: access, RefreshTokenEncrypted: refresh,
				AccessExpiresAt: now.Add(tokens.ExpiresIn), CreatedAt: now, UpdatedAt: now})
		}
	}
	if err != nil {
		s.revokeUnsaved(ctx, clientID, tokens.Refresh)
		return VideoLoginStatus{Status: "error", Message: "The account could not be saved; start a new sign-in"}, 0
	}
	return VideoLoginStatus{Status: "success", Email: identity.Email}, 0
}

// revokeUnsaved ends a session that codex-lb could not keep.
func (s *VideoService) revokeUnsaved(ctx context.Context, clientID, refresh string) {
	revokeCtx, cancel := context.WithTimeout(ctx, videoRevokeTimeout)
	defer cancel()
	_ = s.upstream.Revoke(revokeCtx, clientID, refresh)
}

// CancelLogin forgets a sign-in the administrator closed. A code approved later
// is then never exchanged.
func (s *VideoService) CancelLogin(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.logins, id)
}
