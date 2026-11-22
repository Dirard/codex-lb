package application

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"codex-lb/internal/domain"
)

const (
	antigravitySignInTTL     = 10 * time.Minute
	maxAntigravitySignIns    = 8
	antigravitySignInTimeout = 2 * time.Minute
	maxAntigravityCodeBytes  = 4096
	maxAntigravityActionURL  = 2048
)

var (
	// ErrAntigravityGrantRefused is Google definitively refusing a code or a
	// refresh token (invalid_grant).
	ErrAntigravityGrantRefused = errors.New("google refused the antigravity grant")
	// ErrAntigravityUnauthorized is Cloud Code or userinfo rejecting an
	// access token.
	ErrAntigravityUnauthorized = errors.New("google rejected the antigravity access token")
	// ErrAntigravityAccountUnavailable is an Antigravity source that cannot
	// lend a token: it is gone, needs a new sign-in or was suspended.
	ErrAntigravityAccountUnavailable = errors.New("antigravity account is unavailable")
)

// AntigravityTokenSet are Google tokens on their way to the cipher. Refresh is
// empty when Google keeps the current refresh token.
type AntigravityTokenSet struct {
	Access    string
	Refresh   string
	ExpiresIn time.Duration
}

// AntigravityCodeAssist is what loadCodeAssist says about an account.
type AntigravityCodeAssist struct {
	Project string
	Tier    string
	// OnboardTier is the tier onboardUser takes when no project came back.
	OnboardTier string
}

// AntigravityModel is one model fetchAvailableModels lists for an account,
// internal ids already left out.
type AntigravityModel struct {
	ID              string
	DisplayName     string
	ContextWindow   int64
	MaxOutputTokens int64
	Images          bool
	Thinking        bool
	Disabled        bool
}

// AntigravityUpstream is Google's OAuth and Cloud Code control surface for
// the Antigravity client.
type AntigravityUpstream interface {
	AuthorizationURL(state, codeChallenge string) string
	ExchangeCode(ctx context.Context, code, codeVerifier string) (AntigravityTokenSet, error)
	Refresh(ctx context.Context, refreshToken string) (AntigravityTokenSet, error)
	UserEmail(ctx context.Context, accessToken string) (string, error)
	LoadCodeAssist(ctx context.Context, accessToken string) (AntigravityCodeAssist, error)
	// OnboardUser onboards the account to tierID and returns its project once
	// the operation is done.
	OnboardUser(ctx context.Context, accessToken, tierID string) (string, error)
	AvailableModels(ctx context.Context, accessToken, project string) ([]AntigravityModel, error)
}

// AntigravityStore persists Antigravity sources: a model source, its account
// row and its credential, whose external key is the encrypted refresh token.
type AntigravityStore interface {
	ModelSourceRepository
	GetAccountCredential(ctx context.Context, accountID string) (domain.AccountCredential, error)
	UpdateAccountStickyOnly(ctx context.Context, accountID string, sticky bool) error
	// RotateExternalCredential replaces the encrypted credential only while it
	// still holds previous.
	RotateExternalCredential(ctx context.Context, accountID string, previous, next []byte) (bool, error)
	// SetAntigravityAccountStatus writes the account status with its reason
	// and, when the status changed, the source's action link (nil keeps it).
	SetAntigravityAccountStatus(ctx context.Context, sourceID string, status domain.AccountStatus, reason string, actionURL *string) error
}

// AntigravitySignInStart is what the dashboard shows: the link to open and
// the flow to complete with the pasted code.
type AntigravitySignInStart struct {
	FlowID           string    `json:"flowId"`
	AuthorizationURL string    `json:"authorizationUrl"`
	ExpiresAt        time.Time `json:"expiresAt"`
}

// antigravitySignIn lives in memory for one sign-in; its PKCE verifier never
// leaves the process.
type antigravitySignIn struct {
	state     string
	verifier  string
	sourceID  string
	expiresAt time.Time
}

type antigravityAccessToken struct {
	access  string
	expires time.Time
	version int64
}

type antigravityFlight struct {
	done chan struct{}
	err  error
}

// AntigravityService signs in Google Antigravity accounts as model sources,
// lends their access tokens to the provider and applies the account effects
// of Google's refusals.
type AntigravityService struct {
	store    AntigravityStore
	cipher   SecretCipher
	upstream AntigravityUpstream
	now      func() time.Time
	log      *slog.Logger

	mu      sync.Mutex
	signIns map[string]*antigravitySignIn
	tokens  map[string]antigravityAccessToken
	flights map[string]*antigravityFlight
	version int64
	// saving serializes the local part of a completed sign-in, so two
	// sign-ins of one email cannot create two sources.
	saving sync.Mutex
}

func NewAntigravityService(store AntigravityStore, cipher SecretCipher, upstream AntigravityUpstream) *AntigravityService {
	return &AntigravityService{store: store, cipher: cipher, upstream: upstream, now: time.Now, log: slog.New(slog.DiscardHandler),
		signIns: map[string]*antigravitySignIn{}, tokens: map[string]antigravityAccessToken{}, flights: map[string]*antigravityFlight{}}
}

// ConfigureClock replaces the clock; tests use it to reach expiries.
func (s *AntigravityService) ConfigureClock(now func() time.Time) { s.now = now }

// ConfigureLogger sets where account effects are recorded (ids and codes only).
func (s *AntigravityService) ConfigureLogger(logger *slog.Logger) { s.log = logger }

// StartSignIn realizes the workflow step "start sign-in": it remembers a PKCE
// verifier and state for 10 minutes and returns Google's authorization URL.
// sourceID, when set, names the Antigravity source being re-authorized; the
// sign-in must then come back with that source's email.
func (s *AntigravityService) StartSignIn(ctx context.Context, sourceID string) (AntigravitySignInStart, error) {
	if sourceID != "" {
		source, err := s.store.GetModelSource(ctx, sourceID)
		if errors.Is(err, domain.ErrNotFound) || err == nil && source.Kind != domain.ModelSourceAntigravity {
			return AntigravitySignInStart{}, antigravitySourceNotFound()
		}
		if err != nil {
			return AntigravitySignInStart{}, err
		}
	}
	now := s.now()
	verifier := randomToken(48)
	flow := &antigravitySignIn{state: randomToken(16), verifier: verifier, sourceID: sourceID, expiresAt: now.Add(antigravitySignInTTL)}
	id := "agsign_" + strings.ToLower(rand.Text())
	s.mu.Lock()
	for key, pending := range s.signIns {
		if !now.Before(pending.expiresAt) {
			delete(s.signIns, key)
		}
	}
	if len(s.signIns) >= maxAntigravitySignIns {
		s.mu.Unlock()
		return AntigravitySignInStart{}, &ProxyError{Code: "antigravity_sign_in_limit", Status: 429, Message: "Too many Antigravity sign-ins are in progress; finish one or wait 10 minutes"}
	}
	s.signIns[id] = flow
	s.mu.Unlock()
	return AntigravitySignInStart{FlowID: id, AuthorizationURL: s.upstream.AuthorizationURL(flow.state, pkceChallenge(verifier)), ExpiresAt: flow.expiresAt.UTC()}, nil
}

// CompleteSignIn realizes the workflow steps "exchange it, read the email,
// load the project and tier, list the models": the flow is used once, the
// code is exchanged, and the account becomes a new Antigravity source or
// re-authorizes the source that already has its email.
func (s *AntigravityService) CompleteSignIn(ctx context.Context, flowID, pasted string) (domain.ModelSource, error) {
	flow, err := s.takeSignIn(flowID)
	if err != nil {
		return domain.ModelSource{}, err
	}
	code, err := antigravityCode(pasted, flow.state)
	if err != nil {
		return domain.ModelSource{}, err
	}
	// The code works once, so the exchange and save outlive a request whose
	// browser went away.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), antigravitySignInTimeout)
	defer cancel()
	tokens, err := s.upstream.ExchangeCode(ctx, code, flow.verifier)
	if errors.Is(err, ErrAntigravityGrantRefused) {
		return domain.ModelSource{}, &ProxyError{Code: "antigravity_code_rejected", Status: 400, Param: "code",
			Message: "Google refused this code; it may be mistyped, used or expired. Start a new sign-in"}
	}
	if err != nil || tokens.Refresh == "" {
		return domain.ModelSource{}, antigravityUnavailable("Google did not complete the sign-in; start a new one")
	}
	email, err := s.upstream.UserEmail(ctx, tokens.Access)
	email = strings.TrimSpace(email)
	if err != nil || email == "" || len(email) > 320 {
		return domain.ModelSource{}, antigravityUnavailable("Google did not say which account signed in; start a new sign-in")
	}
	assist, err := s.upstream.LoadCodeAssist(ctx, tokens.Access)
	if err != nil {
		return domain.ModelSource{}, antigravityUnavailable("Antigravity did not load this account; start a new sign-in")
	}
	if assist.Project == "" {
		tier := assist.OnboardTier
		if tier == "" {
			tier = "free-tier"
		}
		if assist.Project, err = s.upstream.OnboardUser(ctx, tokens.Access, tier); err != nil {
			assist.Project = ""
		}
	}
	if assist.Project == "" || len(assist.Project) > 128 {
		return domain.ModelSource{}, &ProxyError{Code: "antigravity_project_unavailable", Status: 502,
			Message: "Antigravity gave this account no Cloud Code project; check that it may use Antigravity"}
	}
	listed, err := s.upstream.AvailableModels(ctx, tokens.Access, assist.Project)
	if err != nil {
		return domain.ModelSource{}, antigravityModelsUnavailable()
	}
	refresh, err := s.cipher.Encrypt([]byte(tokens.Refresh))
	if err != nil {
		return domain.ModelSource{}, errors.New("could not encrypt the Antigravity credential")
	}
	info := domain.AntigravityAccountInfo{Email: email, Project: assist.Project, Tier: truncateBytes(assist.Tier, 64)}
	return s.saveSignIn(ctx, flow.sourceID, info, listed, refresh, tokens)
}

// saveSignIn creates the source of a new email or re-authorizes the source
// that has it. Only local writes happen under the lock.
func (s *AntigravityService) saveSignIn(ctx context.Context, intended string, info domain.AntigravityAccountInfo, listed []AntigravityModel,
	refresh []byte, tokens AntigravityTokenSet) (domain.ModelSource, error) {
	s.saving.Lock()
	defer s.saving.Unlock()
	sources, err := s.store.ListModelSources(ctx)
	if err != nil {
		return domain.ModelSource{}, err
	}
	var existing *domain.ModelSource
	for i := range sources {
		if source := sources[i]; source.Kind == domain.ModelSourceAntigravity && source.ProviderConfig.Antigravity != nil &&
			strings.EqualFold(source.ProviderConfig.Antigravity.Email, info.Email) {
			existing = &sources[i]
			break
		}
	}
	if intended != "" && (existing == nil || existing.ID != intended) {
		return domain.ModelSource{}, &ProxyError{Code: "antigravity_account_mismatch", Status: 409,
			Message: "You signed in with another Google account than the one being re-authorized; nothing was changed"}
	}
	now := s.now().UTC()
	credential := &domain.AccountCredential{ExternalKeyEncrypted: refresh}
	if existing != nil {
		// Re-authorization keeps models, prices, enabled flags and options;
		// only the account, its credential and its status change.
		source := *existing
		source.ProviderConfig.Antigravity = &info
		source.UpdatedAt = now
		credential.AccountID = source.ID
		if err := s.store.SaveModelSource(ctx, source, credential); err != nil {
			return domain.ModelSource{}, err
		}
		cleared := ""
		if err := s.store.SetAntigravityAccountStatus(ctx, source.ID, domain.AccountActive, "", &cleared); err != nil {
			return domain.ModelSource{}, err
		}
		s.remember(source.ID, tokens)
		s.log.Info("antigravity account re-authorized", "source_id", source.ID)
		return s.store.GetModelSource(ctx, source.ID)
	}
	models := make([]domain.ModelSourceModel, 0, len(listed))
	for _, model := range listed {
		if !model.Disabled {
			models = append(models, antigravitySourceModel(model, now))
		}
	}
	if len(models) == 0 {
		return domain.ModelSource{}, antigravityModelsUnavailable()
	}
	source := domain.ModelSource{
		ID: "src_" + rand.Text(), Name: truncateBytes(info.Email, 128), Kind: domain.ModelSourceAntigravity,
		BaseURL: domain.AntigravityBaseURL, Enabled: true, Health: "unknown", Chat: true, Responses: true,
		ProviderConfig: domain.ModelSourceProviderConfig{Antigravity: &info}, Models: models, CreatedAt: now, UpdatedAt: now,
	}
	credential.AccountID = source.ID
	if err := source.Validate(); err != nil {
		return domain.ModelSource{}, err
	}
	// The source's account row starts without an egress requirement, like
	// every saved model source; the provider sends its token only to the
	// fixed Cloud Code endpoint under the model-source egress guard.
	if err := s.store.SaveModelSource(ctx, source, credential); err != nil {
		return domain.ModelSource{}, err
	}
	// Thought signatures and Google's heuristics tie a conversation to its
	// account, so a new Antigravity source keeps its conversations.
	if err := s.store.UpdateAccountStickyOnly(ctx, source.ID, true); err != nil {
		return domain.ModelSource{}, err
	}
	s.remember(source.ID, tokens)
	s.log.Info("antigravity account signed in", "source_id", source.ID)
	return s.store.GetModelSource(ctx, source.ID)
}

// takeSignIn removes a flow, so a code is never exchanged twice.
func (s *AntigravityService) takeSignIn(flowID string) (*antigravitySignIn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	flow := s.signIns[flowID]
	if flow == nil {
		return nil, &ProxyError{Code: "antigravity_sign_in_not_found", Status: 404, Message: "This sign-in is over; start a new one"}
	}
	delete(s.signIns, flowID)
	if !s.now().Before(flow.expiresAt) {
		return nil, &ProxyError{Code: "antigravity_sign_in_expired", Status: 410, Message: "This sign-in expired; start a new one"}
	}
	return flow, nil
}

// antigravityCode accepts the code Google's page shows or the whole callback
// URL; a state in the URL must be this sign-in's.
func antigravityCode(pasted, state string) (string, error) {
	pasted = strings.TrimSpace(pasted)
	if pasted == "" || len(pasted) > maxAntigravityCodeBytes {
		return "", &ProxyError{Code: "invalid_request", Status: 400, Param: "code", Message: "Paste the code Google showed after the sign-in"}
	}
	if !strings.Contains(pasted, "code=") {
		return pasted, nil
	}
	query := pasted
	if parsed, err := url.Parse(pasted); err == nil && parsed.RawQuery != "" {
		query = parsed.RawQuery
	}
	values, err := url.ParseQuery(strings.TrimPrefix(query, "?"))
	code := strings.TrimSpace(values.Get("code"))
	if err != nil || code == "" {
		return "", &ProxyError{Code: "invalid_request", Status: 400, Param: "code", Message: "Paste the code Google showed after the sign-in"}
	}
	if got := values.Get("state"); got != "" && got != state {
		return "", &ProxyError{Code: "antigravity_state_mismatch", Status: 400, Param: "code",
			Message: "This code belongs to another sign-in; start a new one"}
	}
	return code, nil
}

// antigravitySourceModel is a listed model as a free source model: Google
// bills the account's plan, so every price is zero.
func antigravitySourceModel(model AntigravityModel, now time.Time) domain.ModelSourceModel {
	input, cached, output := 0.0, 0.0, 0.0
	display := strings.TrimSpace(model.DisplayName)
	if display == "" {
		display = model.ID
	}
	entry := domain.ModelSourceModel{
		Model: model.ID, DisplayName: truncateBytes(display, 255), ContextWindow: max(model.ContextWindow, 0),
		MaxOutputTokens: max(model.MaxOutputTokens, 0), Streaming: true, Tools: true, Vision: model.Images,
		InputPerMillion: &input, CachedPerMillion: &cached, OutputPerMillion: &output,
		Enabled: true, CreatedAt: now, UpdatedAt: now,
	}
	if model.Thinking {
		entry.RawMetadataJSON = antigravityReasoningMetadata(model.ID)
	}
	return entry
}

// antigravityReasoningMetadata offers Codex the usual efforts; a wire id
// names its thinking tier as a suffix, which becomes the default.
func antigravityReasoningMetadata(id string) string {
	levels := []string{"low", "medium", "high"}
	level := "high"
	switch suffix := id[strings.LastIndex(id, "-")+1:]; suffix {
	case "low", "medium":
		level = suffix
	case "xhigh":
		levels = append(levels, "xhigh")
	}
	metadata, _ := json.Marshal(map[string]any{
		"supports_reasoning": true, "supported_reasoning_levels": levels, "default_reasoning_level": level,
	})
	return string(metadata)
}

// mergeAntigravityModels keeps known models with their prices and enabled
// flags, adds new ones at price zero and disables the ones Google no longer
// lists. The result is ordered like the stored models, new ones last by id.
func mergeAntigravityModels(existing []domain.ModelSourceModel, listed []AntigravityModel, now time.Time) []domain.ModelSourceModel {
	offered := make(map[string]AntigravityModel, len(listed))
	for _, model := range listed {
		if !model.Disabled {
			offered[model.ID] = model
		}
	}
	names := make(map[string]bool)
	merged := make([]domain.ModelSourceModel, 0, len(existing)+len(offered))
	for _, model := range existing {
		names[model.Model] = true
		for _, alias := range model.Aliases {
			names[strings.TrimSpace(alias)] = true
		}
		if _, ok := offered[model.Model]; !ok && model.Enabled {
			model.Enabled = false
			model.UpdatedAt = now
		}
		merged = append(merged, model)
	}
	added := make([]string, 0, len(offered))
	for id := range offered {
		if !names[id] {
			added = append(added, id)
		}
	}
	sort.Strings(added)
	for _, id := range added {
		merged = append(merged, antigravitySourceModel(offered[id], now))
	}
	return merged
}

// safeActionURL keeps only an https link of bounded size for the dashboard.
func safeActionURL(raw string) string {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || len(raw) > maxAntigravityActionURL {
		return ""
	}
	return raw
}

func truncateBytes(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func antigravityUnavailable(message string) error {
	return &ProxyError{Code: "antigravity_unavailable", Status: 502, Message: message}
}

func antigravityModelsUnavailable() error {
	return &ProxyError{Code: "antigravity_models_unavailable", Status: 502, Message: "Antigravity listed no usable models for this account"}
}

func antigravitySourceNotFound() error {
	return &ProxyError{Code: "antigravity_source_not_found", Status: 404, Message: "This Antigravity source no longer exists"}
}
