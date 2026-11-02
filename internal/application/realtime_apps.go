package application

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"strings"
	"sync"
	"time"

	"codex-lb/internal/domain"
)

const (
	// MaxRealtimeSecretBytes bounds a client secret request body.
	MaxRealtimeSecretBytes = 64 << 10
	// RealtimeClientSecretPrefix marks the secrets codex-lb issues to browsers.
	RealtimeClientSecretPrefix = "ek_"
	realtimeSecretDefaultTTL   = 600 * time.Second
	realtimeSecretMinTTL       = 10 * time.Second
	realtimeSecretMaxTTL       = 7200 * time.Second
	defaultRealtimeModel       = "gpt-realtime"
)

// RealtimeClientSecretStore keeps the client secrets browser apps use.
type RealtimeClientSecretStore interface {
	SaveRealtimeClientSecret(context.Context, domain.RealtimeClientSecret) error
	RealtimeClientSecret(context.Context, string, time.Time) (domain.RealtimeClientSecret, error)
}

// RealtimeCredential is what authenticated an app's Realtime request: its
// key and, for a client secret, the session the secret was issued with.
type RealtimeCredential struct {
	KeyID        string
	ClientSecret bool
	Session      json.RawMessage
}

func invalidRealtimeCredential() error {
	return &ProxyError{Code: "invalid_api_key", Status: 401, Message: "Invalid or expired API key"}
}

func (s *CodexOperations) realtimeSecrets() (RealtimeClientSecretStore, error) {
	secrets, ok := s.store.(RealtimeClientSecretStore)
	if !ok || s.cipher == nil {
		return nil, &ProxyError{Code: "realtime_client_secrets_unavailable", Status: 503, Message: "Realtime client secrets are not available"}
	}
	return secrets, nil
}

// IssueRealtimeClientSecret checks the expiry and session of a server's
// request and stores the new secret's hash with its key, session and expiry.
func (s *CodexOperations) IssueRealtimeClientSecret(ctx context.Context, keyID string, body []byte) (CodexOperationResult, error) {
	key, err := s.activeKey(ctx, keyID)
	if err != nil {
		return CodexOperationResult{}, err
	}
	secrets, err := s.realtimeSecrets()
	if err != nil {
		return CodexOperationResult{}, err
	}
	var request struct {
		ExpiresAfter *struct {
			Anchor  string `json:"anchor"`
			Seconds int64  `json:"seconds"`
		} `json:"expires_after"`
		Session json.RawMessage `json:"session"`
	}
	if len(bytes.TrimSpace(body)) != 0 && json.Unmarshal(body, &request) != nil {
		return CodexOperationResult{}, &ProxyError{Code: "invalid_request", Status: 400, Message: "Invalid client secret request"}
	}
	ttl := realtimeSecretDefaultTTL
	if after := request.ExpiresAfter; after != nil {
		ttl = time.Duration(after.Seconds) * time.Second
		if after.Anchor != "" && after.Anchor != "created_at" || ttl < realtimeSecretMinTTL || ttl > realtimeSecretMaxTTL {
			return CodexOperationResult{}, &ProxyError{Code: "invalid_request", Status: 400, Message: "expires_after must be 10 to 7200 seconds after created_at", Param: "expires_after"}
		}
	}
	session := json.RawMessage("{}")
	var encrypted []byte
	if len(request.Session) != 0 && string(request.Session) != "null" {
		if !jsonObject(request.Session) {
			return CodexOperationResult{}, &ProxyError{Code: "invalid_request", Status: 400, Message: "session must be an object", Param: "session"}
		}
		session = request.Session
		if encrypted, err = s.cipher.Encrypt(request.Session); err != nil {
			return CodexOperationResult{}, errors.New("could not encrypt realtime session")
		}
	}
	value := RealtimeClientSecretPrefix + strings.ToLower(rand.Text()+rand.Text())
	now := s.now().UTC()
	secret := domain.RealtimeClientSecret{Hash: realtimeSecretHash(value), APIKeyID: key.ID, SessionEncrypted: encrypted, ExpiresAt: now.Add(ttl), CreatedAt: now}
	if err := secrets.SaveRealtimeClientSecret(ctx, secret); err != nil {
		return CodexOperationResult{}, err
	}
	answer, _ := json.Marshal(struct {
		Value     string          `json:"value"`
		ExpiresAt int64           `json:"expires_at"`
		Session   json.RawMessage `json:"session"`
	}{value, secret.ExpiresAt.Unix(), session})
	return CodexOperationResult{Status: 200, Body: answer, ContentType: "application/json"}, nil
}

// ResolveRealtimeClientSecret turns an unexpired client secret of an active
// key into that key and the secret's session.
func (s *CodexOperations) ResolveRealtimeClientSecret(ctx context.Context, value string) (RealtimeCredential, error) {
	secrets, err := s.realtimeSecrets()
	if err != nil {
		return RealtimeCredential{}, err
	}
	if !strings.HasPrefix(value, RealtimeClientSecretPrefix) || len(value) > 256 {
		return RealtimeCredential{}, invalidRealtimeCredential()
	}
	secret, err := secrets.RealtimeClientSecret(ctx, realtimeSecretHash(value), s.now().UTC())
	if errors.Is(err, domain.ErrNotFound) {
		return RealtimeCredential{}, invalidRealtimeCredential()
	}
	if err != nil {
		return RealtimeCredential{}, err
	}
	if _, err := s.activeKey(ctx, secret.APIKeyID); err != nil {
		return RealtimeCredential{}, err
	}
	credential := RealtimeCredential{KeyID: secret.APIKeyID, ClientSecret: true}
	if len(secret.SessionEncrypted) != 0 {
		plain, err := s.cipher.Decrypt(secret.SessionEncrypted)
		if err != nil {
			return RealtimeCredential{}, invalidRealtimeCredential()
		}
		credential.Session = plain
	}
	return credential, nil
}

func realtimeSecretHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func jsonObject(raw json.RawMessage) bool {
	var object map[string]json.RawMessage
	return json.Unmarshal(raw, &object) == nil && object != nil
}

// CreateAppRealtimeCall builds the backend's JSON call from an app's raw SDP,
// multipart or JSON offer and creates it on a subscription account.
func (s *CodexOperations) CreateAppRealtimeCall(ctx context.Context, options CodexOperationOptions, credential RealtimeCredential,
	contentType string, body []byte, model string, headers CodexRealtimeHeaders) (CodexOperationResult, error) {
	if len(body) == 0 || len(body) > MaxRealtimeCallBytes {
		return CodexOperationResult{}, &ProxyError{Code: "invalid_request", Status: 400, Message: "Invalid realtime call request"}
	}
	if model == "" {
		model = defaultRealtimeModel
	}
	if !domain.ValidRealtimeModel(model) {
		return CodexOperationResult{}, &ProxyError{Code: "invalid_request", Status: 400, Message: "Invalid realtime model", Param: "model"}
	}
	sdp, session, err := realtimeOffer(contentType, body)
	if err != nil {
		return CodexOperationResult{}, err
	}
	switch {
	case session != nil:
	case credential.Session != nil:
		session = credential.Session
	default:
		session, _ = json.Marshal(map[string]string{"type": "realtime", "model": model})
	}
	call, _ := json.Marshal(struct {
		SDP     string          `json:"sdp"`
		Session json.RawMessage `json:"session"`
	}{sdp, session})
	result, err := s.CreateRealtimeCall(ctx, options, CodexControlRequest{Body: call, ContentType: "application/json", RealtimeHeaders: headers})
	if err != nil {
		return result, err
	}
	callID := realtimeCallID(resultHeaderValue(result.Headers, "Location"))
	result.ContentType = "application/sdp"
	result.Headers = map[string][]string{"Location": {"/v1/realtime/calls/" + callID}}
	return result, nil
}

// realtimeOffer reads the SDP and optional session of an app's offer; the
// subscription backend itself accepts only JSON.
func realtimeOffer(contentType string, body []byte) (string, json.RawMessage, error) {
	unsupported := &ProxyError{Code: "invalid_content_type", Status: 415, Message: "Unsupported realtime call content type"}
	invalid := &ProxyError{Code: "invalid_request", Status: 400, Message: "Invalid realtime call request"}
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "", nil, unsupported
	}
	var sdp string
	var session json.RawMessage
	switch mediaType {
	case "application/sdp":
		sdp = string(body)
	case "application/json":
		var offer struct {
			SDP     string          `json:"sdp"`
			Session json.RawMessage `json:"session"`
		}
		if json.Unmarshal(body, &offer) != nil {
			return "", nil, invalid
		}
		sdp, session = offer.SDP, offer.Session
	case "multipart/form-data":
		if params["boundary"] == "" {
			return "", nil, unsupported
		}
		reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
		for {
			part, err := reader.NextPart()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return "", nil, invalid
			}
			value, err := io.ReadAll(io.LimitReader(part, MaxRealtimeCallBytes))
			if err != nil {
				return "", nil, invalid
			}
			switch part.FormName() {
			case "sdp":
				sdp = string(value)
			case "session":
				session = value
			}
		}
	default:
		return "", nil, unsupported
	}
	if strings.TrimSpace(sdp) == "" || len(session) != 0 && string(session) != "null" && !jsonObject(session) {
		return "", nil, invalid
	}
	if string(session) == "null" || len(session) == 0 {
		session = nil
	}
	return sdp, session, nil
}

// RealtimeSessionPlan is an app session checked before the client's upgrade:
// its key, the subscription account and the upstream request.
type RealtimeSessionPlan struct {
	key     domain.APIKey
	account domain.Account
	request CodexRealtimeSessionRequest
}

// PrepareRealtimeSession checks an app session before the client's upgrade:
// the request bounds, the key and an available subscription account.
func (s *CodexOperations) PrepareRealtimeSession(ctx context.Context, options CodexOperationOptions, credential RealtimeCredential,
	request CodexRealtimeSessionRequest) (RealtimeSessionPlan, error) {
	if err := ValidateRealtimeSessionRequest(request); err != nil {
		return RealtimeSessionPlan{}, err
	}
	key, err := s.activeKey(ctx, options.KeyID)
	if err != nil {
		return RealtimeSessionPlan{}, err
	}
	if _, ok := s.provider.(CodexRealtimeSessionProvider); !ok {
		return RealtimeSessionPlan{}, &ProxyError{Code: "realtime_session_unavailable", Status: 503, Message: "Realtime sessions are not available"}
	}
	if credential.Session != nil {
		request.FirstMessage, _ = json.Marshal(struct {
			Type    string          `json:"type"`
			Session json.RawMessage `json:"session"`
		}{"session.update", credential.Session})
	}
	account, err := s.accountForOwner(ctx, key.ID, nil, nil)
	if err != nil {
		return RealtimeSessionPlan{}, err
	}
	return RealtimeSessionPlan{key: key, account: account, request: request}, nil
}

// RealtimeSession serves a prepared app session: it reserves the session
// budget on the key, relays the account's session and settles the summed
// response usage once.
func (s *CodexOperations) RealtimeSession(ctx context.Context, options CodexOperationOptions, plan RealtimeSessionPlan, connection CodexRealtimeConnection) error {
	sessions, ok := s.provider.(CodexRealtimeSessionProvider)
	if !ok || plan.key.ID == "" {
		return &ProxyError{Code: "realtime_session_unavailable", Status: 503, Message: "Realtime sessions are not available"}
	}
	key, account, request := plan.key, plan.account, plan.request
	started := s.now()
	release, err := s.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	id, attemptAt := "req_"+rand.Text(), s.now()
	if _, err := s.store.ReserveUsage(ctx, domain.ReservationRequest{
		ID: id, APIKeyID: key.ID, AccountID: account.ID, AccountGeneration: account.Generation, RouteRevision: account.RouteRevision,
		Model: request.Model, Budget: domain.RealtimeSessionBudget(request.Model), Now: attemptAt,
	}); err != nil {
		return reservationError(err)
	}
	var mu sync.Mutex
	var total domain.UsageAmount
	invalidUsage := false
	observe := func(data []byte) {
		if !bytes.Contains(data, []byte(`"response.done"`)) {
			return
		}
		var event struct {
			Type     string `json:"type"`
			Response struct {
				Usage *domain.RealtimeUsage `json:"usage"`
			} `json:"response"`
		}
		if json.Unmarshal(data, &event) != nil || event.Type != "response.done" || event.Response.Usage == nil {
			return
		}
		amount, err := event.Response.Usage.Amount(request.Model)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			invalidUsage = true
			return
		}
		total.InputTokens += amount.InputTokens
		total.OutputTokens += amount.OutputTokens
		total.CachedInputTokens += amount.CachedInputTokens
		total.CostMicrodollars += amount.CostMicrodollars
	}
	target := CodexOperationTarget{Account: account, KeyID: key.ID, SessionID: options.upstreamSessionID(account.ID), accountNeutral: true}
	callErr := sessions.RealtimeSession(ctx, target, request, connection, observe)
	mu.Lock()
	usage, invalid := total, invalidUsage
	mu.Unlock()
	status, result := "success", CodexOperationResult{Status: 200}
	if callErr != nil {
		status, result = "error", CodexOperationResult{Status: 502, ErrorCode: operationErrorCode(callErr)}
		if cancelledCall(callErr) {
			status, result.ErrorCode = "cancelled", "request_cancelled"
		}
	}
	if invalid && result.ErrorCode == "" {
		result.ErrorCode = "invalid_upstream_usage"
	}
	event := s.usageEvent(options, account, request.Model, "realtime_session", status, result, usage, id, started, attemptAt)
	settlement := "finalized"
	if status != "success" {
		settlement = "failed"
	}
	if err := s.settle(ctx, id, domain.UsageSettlement{Status: settlement, Event: event}); err != nil {
		return err
	}
	return callErr
}

// ValidateRealtimeSessionRequest bounds an app session's model, query values
// and negotiation headers before any upgrade.
func ValidateRealtimeSessionRequest(request CodexRealtimeSessionRequest) error {
	if !domain.ValidRealtimeModel(request.Model) {
		return &ProxyError{Code: "invalid_request", Status: 400, Message: "Provide a valid realtime model", Param: "model"}
	}
	if err := request.Headers.Validate(); err != nil {
		return err
	}
	if len(request.Query) > 16 {
		return &ProxyError{Code: "invalid_request", Status: 400, Message: "Realtime query is too large"}
	}
	for _, pair := range request.Query {
		if pair[0] == "call_id" || pair[0] == "model" || len(pair[0]) > 512 || len(pair[1]) > 512 {
			return &ProxyError{Code: "invalid_request", Status: 400, Message: "Invalid realtime query"}
		}
	}
	return nil
}
