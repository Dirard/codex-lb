package application

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net/url"
	"strings"
	"sync"
	"time"

	"codex-lb/internal/domain"
)

const (
	browserFlowTTL      = 15 * time.Minute
	maxTerminalFlows    = 16
	callbackHost        = "127.0.0.1"
	callbackPort        = "1455"
	seatMismatchMessage = "The account you signed in as is not the one being re-authenticated. No changes were made."
)

type oauthFlow struct {
	id                string
	status            string // pending | success | error
	method            string // browser | device
	errorMessage      string
	stateToken        string
	codeVerifier      string
	intendedAccountID string
	deletedAccounts   map[string]bool // Only while pending; protected by flows.mu.
	deviceAuthID      string
	userCode          string
	intervalSeconds   int
	expiresAt         time.Time
	finishedAt        time.Time
	pollCancel        context.CancelFunc
	processing        bool
	expiryTimer       *time.Timer
}

type oauthFlowStore struct {
	mu             sync.Mutex
	flows          map[string]*oauthFlow
	byState        map[string]string
	latest         string
	deviceCurrent  string
	latestFlowless struct {
		status string
		error  string
	}
	callbackServer io.Closer
	pendingBrowser int
	terminal       int
	closed         bool
	jobs           sync.WaitGroup
}

type OAuthStartResult struct {
	FlowID           *string `json:"flowId"`
	Method           string  `json:"method"`
	AuthorizationURL *string `json:"authorizationUrl"`
	CallbackURL      *string `json:"callbackUrl"`
	VerificationURL  *string `json:"verificationUrl"`
	UserCode         *string `json:"userCode"`
	DeviceAuthID     *string `json:"deviceAuthId"`
	IntervalSeconds  *int    `json:"intervalSeconds"`
	ExpiresInSeconds *int    `json:"expiresInSeconds"`
}

type OAuthStatusResult struct {
	Status       string  `json:"status"`
	ErrorMessage *string `json:"errorMessage"`
}

func (s *AccountsService) StartOAuth(ctx context.Context, forceMethod, accountID string) (OAuthStartResult, error) {
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	defer cancel()
	s.flows.mu.Lock()
	if s.flows.closed {
		s.flows.mu.Unlock()
		return OAuthStartResult{}, context.Canceled
	}
	s.pruneExpiredLocked(s.now())
	s.flows.mu.Unlock()
	if forceMethod == "" && accountID == "" {
		accounts, err := s.accounts.ListAccounts(ctx)
		if err != nil {
			return OAuthStartResult{}, err
		}
		if len(accounts) > 0 {
			s.flows.mu.Lock()
			s.flows.latestFlowless.status = "success"
			s.flows.latestFlowless.error = ""
			s.flows.latest = ""
			s.flows.mu.Unlock()
			return OAuthStartResult{Method: "browser"}, nil
		}
	}
	if forceMethod == "device" {
		return s.startDeviceFlow(ctx, accountID)
	}
	return s.startBrowserFlow(ctx, accountID)
}

func (s *AccountsService) startBrowserFlow(ctx context.Context, accountID string) (OAuthStartResult, error) {
	flowID := randomToken(12)
	verifier := randomToken(32)
	challenge := pkceChallenge(verifier)
	state := randomToken(16)
	now := s.now()
	flow := &oauthFlow{
		id: flowID, status: "pending", method: "browser", stateToken: state,
		codeVerifier: verifier, intendedAccountID: accountID, expiresAt: now.Add(browserFlowTTL),
	}
	s.flows.mu.Lock()
	if s.flows.closed {
		s.flows.mu.Unlock()
		return OAuthStartResult{}, context.Canceled
	}
	if len(s.flows.flows)-s.flows.terminal >= 32 {
		s.flows.mu.Unlock()
		return OAuthStartResult{}, domain.ErrConflict
	}
	s.rememberFlowLocked(flow)
	s.startCallbackServerLocked()
	s.flows.mu.Unlock()
	return OAuthStartResult{
		FlowID:           &flowID,
		Method:           "browser",
		AuthorizationURL: stringPtr(s.oauth.AuthorizationURL(state, challenge)),
		CallbackURL:      stringPtr("http://" + callbackHost + ":" + callbackPort + "/auth/callback"),
		ExpiresInSeconds: intPtr(int(browserFlowTTL / time.Second)),
	}, nil
}

func (s *AccountsService) startDeviceFlow(ctx context.Context, accountID string) (OAuthStartResult, error) {
	device, err := s.oauth.RequestDeviceCode(ctx)
	if err != nil {
		return OAuthStartResult{}, err
	}
	now := s.now()
	flow := &oauthFlow{
		id: randomToken(12), status: "pending", method: "device",
		deviceAuthID: device.DeviceAuthID, userCode: device.UserCode,
		intervalSeconds: device.IntervalSeconds, intendedAccountID: accountID,
		expiresAt: now.Add(time.Duration(device.ExpiresInSeconds) * time.Second),
	}
	pollCtx, cancel := context.WithCancel(s.ctx)
	flow.pollCancel = cancel
	s.flows.mu.Lock()
	if s.flows.closed {
		s.flows.mu.Unlock()
		cancel()
		return OAuthStartResult{}, context.Canceled
	}
	if current := s.flows.flows[s.flows.deviceCurrent]; current != nil && current.status == "pending" {
		if current.pollCancel != nil {
			current.pollCancel()
		}
		s.finishFlowLocked(current, "error", "Superseded by a newer device login.")
	}
	if len(s.flows.flows)-s.flows.terminal >= 32 {
		s.flows.mu.Unlock()
		cancel()
		return OAuthStartResult{}, domain.ErrConflict
	}
	s.rememberFlowLocked(flow)
	s.flows.deviceCurrent = flow.id
	s.flows.jobs.Add(1)
	s.flows.mu.Unlock()
	go func() { defer s.flows.jobs.Done(); s.pollDeviceTokens(pollCtx, flow) }()
	return OAuthStartResult{
		FlowID:           &flow.id,
		Method:           "device",
		VerificationURL:  &device.VerificationURL,
		UserCode:         &device.UserCode,
		DeviceAuthID:     &device.DeviceAuthID,
		IntervalSeconds:  &device.IntervalSeconds,
		ExpiresInSeconds: &device.ExpiresInSeconds,
	}, nil
}

func (s *AccountsService) OAuthStatus(ctx context.Context, flowID string) OAuthStatusResult {
	now := s.now()
	s.flows.mu.Lock()
	defer s.flows.mu.Unlock()
	s.pruneExpiredLocked(now)
	if flowID == "" && s.flows.latest != "" {
		flowID = s.flows.latest
	}
	flow := s.flows.flows[flowID]
	if flow == nil {
		if flowID == "" && s.flows.latestFlowless.status != "" {
			return statusResult(s.flows.latestFlowless.status, s.flows.latestFlowless.error)
		}
		return statusResult("pending", "")
	}
	return statusResult(flow.status, flow.errorMessage)
}

func (s *AccountsService) CompleteOAuth(ctx context.Context, flowID, deviceAuthID, userCode string) OAuthStatusResult {
	now := s.now()
	s.flows.mu.Lock()
	defer s.flows.mu.Unlock()
	s.pruneExpiredLocked(now)
	flow := s.flows.flows[flowID]
	if flow == nil && flowID == "" {
		flow = s.flows.flows[s.flows.latest]
	}
	if flow == nil {
		return statusResult("pending", "")
	}
	if deviceAuthID != "" && deviceAuthID != flow.deviceAuthID || userCode != "" && userCode != flow.userCode {
		return statusResult("error", "Device code does not match this login flow.")
	}
	if flow.method == "device" && flow.status != "pending" && flowID != "" {
		return statusResult(flow.status, flow.errorMessage)
	}
	if flow.method == "browser" && flow.status == "success" {
		return statusResult("success", "")
	}
	return statusResult("pending", "")
}

func (s *AccountsService) ManualCallback(ctx context.Context, callbackURL string, flowID string) OAuthStatusResult {
	parsed, err := url.Parse(callbackURL)
	if err != nil {
		return statusResult("error", "Invalid OAuth callback: state mismatch or missing code.")
	}
	query := parsed.Query()
	errorCode := query.Get("error")
	code := query.Get("code")
	state := query.Get("state")
	s.flows.mu.Lock()
	if s.flows.closed {
		s.flows.mu.Unlock()
		return statusResult("error", "Server is shutting down.")
	}
	s.pruneExpiredLocked(s.now())
	flow := s.flowByStateLocked(state)
	if flow != nil && flowID != "" && flow.id != flowID {
		flow = nil
	}
	canRecordError := flow != nil
	if flow != nil && flow.status != "pending" {
		result := statusResult(flow.status, flow.errorMessage)
		s.flows.mu.Unlock()
		return result
	}
	if flow != nil && flow.processing {
		s.flows.mu.Unlock()
		return statusResult("pending", "")
	}
	claimed := flow != nil && errorCode == "" && code != "" && state != "" && flow.codeVerifier != ""
	if claimed {
		flow.processing = true
		s.flows.jobs.Add(1)
	}
	s.flows.mu.Unlock()
	if claimed {
		defer s.flows.jobs.Done()
	}
	if errorCode != "" {
		message := "OAuth error: " + errorCode
		if canRecordError {
			s.recordFlowError(flow, message)
		}
		return statusResult("error", message)
	}
	if code == "" || state == "" || flow == nil || flow.codeVerifier == "" {
		message := "Invalid OAuth callback: state mismatch or missing code."
		if canRecordError {
			s.recordFlowError(flow, message)
		}
		return statusResult("error", message)
	}
	exchangeCtx, cancel := context.WithTimeout(ctx, min(30*time.Second, max(time.Millisecond, flow.expiresAt.Sub(s.now()))))
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	defer cancel()
	tokens, err := s.oauth.ExchangeCode(exchangeCtx, code, flow.codeVerifier)
	if err != nil {
		message := oauthErrorMessage(err)
		s.recordFlowError(flow, message)
		return statusResult("error", message)
	}
	if exchangeCtx.Err() != nil {
		s.recordFlowError(flow, "OAuth exchange was cancelled.")
		return statusResult("error", "OAuth exchange was cancelled.")
	}
	s.flows.mu.Lock()
	if s.flows.closed || flow.status != "pending" {
		result := statusResult("error", "Login flow is no longer active.")
		s.flows.mu.Unlock()
		return result
	}
	err = s.persistTokens(exchangeCtx, tokens, flow)
	s.flows.mu.Unlock()
	if err != nil {
		message := "An internal error occurred."
		if errors.Is(err, domain.ErrConflict) {
			message = "Multiple accounts match the authenticated identity. Remove duplicate accounts and retry OAuth."
		} else if errors.Is(err, errLoginAccountDeleted) {
			message = "Account was deleted while this login was pending. Start a new login."
		} else if err == errSeatMismatch {
			message = seatMismatchMessage
		}
		s.recordFlowError(flow, message)
		return statusResult("error", message)
	}
	s.finishFlow(flow, "success", "")
	return statusResult("success", "")
}

var errSeatMismatch = errors.New("oauth reauth seat mismatch")
var errLoginAccountDeleted = errors.New("account deleted during login")

func (s *AccountsService) pollDeviceTokens(ctx context.Context, flow *oauthFlow) {
	interval := time.Duration(max(flow.intervalSeconds, 1)) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		tokens, ready, err := s.oauth.ExchangeDeviceToken(ctx, flow.deviceAuthID, flow.userCode)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			s.recordFlowError(flow, oauthErrorMessage(err))
			return
		}
		if ready {
			s.flows.mu.Lock()
			if s.flows.closed || s.flows.deviceCurrent != flow.id || flow.status != "pending" {
				s.flows.mu.Unlock()
				return
			}
			err := s.persistTokens(ctx, tokens, flow)
			s.flows.mu.Unlock()
			if err != nil {
				message := "An internal error occurred."
				if err == errSeatMismatch {
					message = seatMismatchMessage
				} else if errors.Is(err, errLoginAccountDeleted) {
					message = "Account was deleted while this login was pending. Start a new login."
				}
				s.recordFlowError(flow, message)
				return
			}
			s.finishFlow(flow, "success", "")
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if s.now().After(flow.expiresAt) {
				s.recordFlowError(flow, "Device code expired.")
				return
			}
		}
	}
}

// The flow lock serializes persistence with account deletion, not with OAuth I/O.
func (s *AccountsService) persistTokens(ctx context.Context, tokens OAuthTokens, flow *oauthFlow) error {
	intendedAccountID := flow.intendedAccountID
	fileTokens := authFileTokens{
		IDToken: tokens.IDToken, AccessToken: tokens.AccessToken, RefreshToken: tokens.RefreshToken,
	}
	account, credential, err := s.accountFromTokens(fileTokens, intendedAccountID)
	if err != nil {
		return err
	}
	if flow.deletedAccounts[account.ID] || intendedAccountID != "" && flow.deletedAccounts[intendedAccountID] {
		return errLoginAccountDeleted
	}
	callbackClaims := parseIDTokenClaims(tokens.IDToken)
	if intendedAccountID == "" {
		_, err := s.persistImportedAccount(ctx, account, credential)
		return err
	}
	intended, err := s.visible(ctx, intendedAccountID)
	if err != nil {
		return errSeatMismatch
	}
	if !s.seatMatches(ctx, intended, account, callbackClaims) {
		return errSeatMismatch
	}
	account.ID = intended.ID
	account.CreatedAt = intended.CreatedAt
	credential.AccountID = intended.ID
	return s.accounts.SaveAccountIdentity(ctx, account, credential)
}

func (s *AccountsService) seatMatches(ctx context.Context, intended, callback domain.Account, callbackClaims idTokenClaims) bool {
	intendedSeats := seatIDSet(intended.ChatGPTUserID)
	if credential, err := s.accounts.GetAccountCredential(ctx, intended.ID); err == nil && len(credential.IDTokenEncrypted) > 0 {
		if plaintext, decryptErr := s.cipher.Decrypt(credential.IDTokenEncrypted); decryptErr == nil {
			claims := parseIDTokenClaims(string(plaintext))
			intendedSeats[claims.chatgptUserID] = true
			intendedSeats[claims.sub] = true
		}
	}
	callbackSeats := seatIDSet(callback.ChatGPTUserID, callbackClaims.chatgptUserID, callbackClaims.sub)
	workspaceMatches := intended.ChatGPTAccountID == "" || intended.ChatGPTAccountID == callback.ChatGPTAccountID
	intendedWorkspace := firstNonEmptyString(intended.WorkspaceID, intended.WorkspaceLabel)
	if intended.ChatGPTAccountID == "" && intendedWorkspace != "" {
		workspaceMatches = firstNonEmptyString(callback.WorkspaceID, callback.WorkspaceLabel) == intendedWorkspace
	}
	seatMatches := false
	for seat := range intendedSeats {
		if callbackSeats[seat] {
			seatMatches = true
			break
		}
	}
	return workspaceMatches && seatMatches && len(intendedSeats) > 0 && len(callbackSeats) > 0
}

func seatIDSet(values ...string) map[string]bool {
	result := make(map[string]bool)
	for _, value := range values {
		if cleaned := strings.TrimSpace(value); cleaned != "" {
			result[cleaned] = true
		}
	}
	return result
}

func statusResult(status, message string) OAuthStatusResult {
	var errorMessage *string
	if message != "" {
		errorMessage = &message
	}
	return OAuthStatusResult{Status: status, ErrorMessage: errorMessage}
}

func oauthErrorMessage(err error) string {
	var oauthErr *OAuthError
	if errors.As(err, &oauthErr) && oauthErr.Message != "" {
		return oauthErr.Message
	}
	return "An internal error occurred."
}

func pkceChallenge(verifier string) string {
	digest := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func randomToken(bytes int) string {
	entropy := make([]byte, bytes)
	rand.Read(entropy)
	return base64.RawURLEncoding.EncodeToString(entropy)
}

func stringPtr(value string) *string { return &value }
func intPtr(value int) *int          { return &value }
