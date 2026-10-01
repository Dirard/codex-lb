package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"codex-lb/internal/domain"
	"codex-lb/internal/domain/pricing"
)

const (
	CodexResourceFile       = "file"
	CodexResourceRealtime   = "realtime_call"
	MaxRealtimeMessageBytes = 4 << 20
	MaxRealtimeCallBytes    = 1 << 20
	transcriptionModel      = "gpt-4o-transcribe"
	maxCompactBody          = 32 << 20
	transcriptionMaxSeconds = 120
)

type CodexOperationTarget struct {
	Account              domain.Account
	KeyID                string
	SessionID            string
	TurnState            string
	ConfirmedOwner       bool
	OnFirstUpstreamEvent func()
	admissionLease       *accountLease
	affinity             *requestAffinity
}

type CodexControlRequest struct {
	Method          string
	Path            string
	Query           [][2]string
	Body            []byte
	ContentType     string
	RealtimeHeaders CodexRealtimeHeaders
}

type CodexTranscriptionRequest struct {
	Model       string
	Audio       []byte
	Filename    string
	ContentType string
	Prompt      string
	Fields      [][2]string
}

type CodexOperationResult struct {
	Status            int
	Body              []byte
	ContentType       string
	Headers           map[string][]string
	Usage             domain.UsageAmount
	UsageKnown        bool
	UsageReported     bool
	OutputObserved    bool
	ServiceTier       string
	AudioSeconds      float64
	AudioSecondsKnown bool
	Failed            bool
	ErrorCode         string
}

type CodexOperationProvider interface {
	Compact(context.Context, CodexOperationTarget, json.RawMessage) (CodexOperationResult, error)
	Control(context.Context, CodexOperationTarget, CodexControlRequest) (CodexOperationResult, error)
	CreateFile(context.Context, CodexOperationTarget, json.RawMessage) (CodexOperationResult, error)
	FinalizeFile(context.Context, CodexOperationTarget, string) (CodexOperationResult, error)
	Transcribe(context.Context, CodexOperationTarget, CodexTranscriptionRequest) (CodexOperationResult, error)
	Realtime(context.Context, CodexOperationTarget, CodexRealtimeRequest, CodexRealtimeConnection) error
}

type CodexRealtimeRequest struct {
	CallID   string
	Query    [][2]string
	Protocol string
	Headers  CodexRealtimeHeaders
}

const (
	CodexRealtimeLive   = "live_v3"
	CodexRealtimeLegacy = "realtime_v1_v2"
)

// Protocol negotiation only; credentials and account identity are provider-owned.
type CodexRealtimeHeaders struct {
	Alpha string
	Beta  string
}

func (h CodexRealtimeHeaders) Validate() error {
	for _, value := range []string{h.Alpha, h.Beta} {
		if len(value) > 1024 || strings.IndexFunc(value, func(char rune) bool { return char < 0x20 && char != '\t' || char == 0x7f }) >= 0 {
			return &ProxyError{Code: "invalid_realtime_headers", Status: 400, Message: "Invalid realtime negotiation headers"}
		}
	}
	return nil
}

type CodexRealtimeMessage struct {
	Binary bool
	Data   []byte
}

type CodexRealtimeConnection interface {
	Read(context.Context) (CodexRealtimeMessage, error)
	Write(context.Context, CodexRealtimeMessage) error
	Close(string, string) error
}

type CodexAdmission interface {
	Acquire(context.Context) (func(), error)
}

type CodexOperationOptions struct {
	KeyID                 string
	SessionID             string
	ThreadID              string
	ClientAffinity        string
	TurnState             string
	SynthesizedTurnState  bool
	ConversationID        string
	UserAgent             string
	UserAgentGroup        string
	ClientIP              string
	Transport             string
	CodexResponsesTrigger bool
	OnDispatch            func()
}

type CodexResourceOwner struct {
	ResourceType      string
	ResourceID        string
	KeyID             string
	AccountID         string
	AccountGeneration int64
	RouteRevision     int64
	ExpiresAt         time.Time
}

type CodexResourceOwners interface {
	SaveCodexResourceOwner(context.Context, CodexResourceOwner) error
	GetCodexResourceOwner(context.Context, string, string, string, time.Time) (CodexResourceOwner, error)
}

type CodexOperationStore interface {
	Accounts
	APIKeys
	UsageLedger
	Settings
	Continuations
	ModelSourceRepository
	AffinityRoutingStore
	// ScopedAccountForOwner checks current key/group/source scope without
	// excluding an established ChatGPT owner solely due quota telemetry.
	ScopedAccountForOwner(context.Context, string, string, time.Time) (domain.Account, error)
	RecordCodexContentFreeUsage(context.Context, domain.UsageEvent) error
}

type CodexOperations struct {
	store                 CodexOperationStore
	owners                CodexResourceOwners
	provider              CodexOperationProvider
	now                   func() time.Time
	ttl                   time.Duration
	admission             CodexAdmission
	resolvePrice          func(context.Context, domain.Account, string) (pricing.Price, error)
	Diagnostics           DiagnosticRecorder
	Catalog               ModelCatalogRoutingPolicy
	selectCompact         func(context.Context, string, *responseRequest, domain.RuntimeSettings, domain.Account, map[string]bool, ...string) (domain.Account, *accountLease, error)
	cipher                SecretCipher
	accountAdmissionProxy *Proxy
}

func NewCodexOperations(store CodexOperationStore, owners CodexResourceOwners, provider CodexOperationProvider, ttl time.Duration) *CodexOperations {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &CodexOperations{
		store: store, owners: owners, provider: provider, now: time.Now, ttl: ttl,
		admission: blockedCodexAdmission{}, resolvePrice: defaultCodexPrice(store),
	}
}

// ConfigureAdmission installs the shared Proxy admission hook. It must run
// before the first operation and is wired by the composition root.
func (s *CodexOperations) ConfigureAdmission(admission CodexAdmission) { s.admission = admission }

// ConfigureAccountSelection shares the live Responses balancer and its state;
// compact never creates an independent first-eligible routing fallback.
func (s *CodexOperations) ConfigureAccountSelection(proxy *Proxy) {
	s.accountAdmissionProxy = proxy
	s.cipher = proxy.cipher
	s.selectCompact = func(ctx context.Context, keyID string, request *responseRequest, settings domain.RuntimeSettings, previous domain.Account, excluded map[string]bool, preferredAccountID ...string) (domain.Account, *accountLease, error) {
		account, _, lease, err := proxy.selectAccountAdmitted(ctx, keyID, request, settings, CapabilityRoute{}, nil, previous, excluded, false, true, preferredAccountID...)
		return account, lease, err
	}
}

func (s *CodexOperations) ConfigurePrice(resolve func(context.Context, domain.Account, string) (pricing.Price, error)) {
	if resolve != nil {
		s.resolvePrice = resolve
	}
}

func (s *CodexOperations) ConfigureDiagnostics(recorder DiagnosticRecorder) { s.Diagnostics = recorder }

func (s *CodexOperations) Compact(ctx context.Context, keyID, sessionID, turnState string, body json.RawMessage) (CodexOperationResult, error) {
	return s.CompactWithOptions(ctx, CodexOperationOptions{KeyID: keyID, SessionID: sessionID, TurnState: turnState}, body)
}

func (s *CodexOperations) Control(ctx context.Context, keyID, sessionID, turnState string, request CodexControlRequest) (CodexOperationResult, error) {
	return s.ControlWithOptions(ctx, CodexOperationOptions{KeyID: keyID, SessionID: sessionID, TurnState: turnState}, request)
}

func (s *CodexOperations) ControlWithOptions(ctx context.Context, options CodexOperationOptions, request CodexControlRequest) (CodexOperationResult, error) {
	if err := validateControlRequest(request); err != nil {
		return CodexOperationResult{}, err
	}
	if _, err := s.activeKey(ctx, options.KeyID); err != nil {
		return CodexOperationResult{}, err
	}
	resolved, err := s.resolveConversationOwner(ctx, options)
	if err != nil {
		return CodexOperationResult{}, err
	}
	account, err := s.accountForOwner(ctx, options.KeyID, resolved.Owner, nil)
	if err != nil {
		return CodexOperationResult{}, err
	}
	target := CodexOperationTarget{Account: account, KeyID: options.KeyID,
		SessionID: options.upstreamSessionID(account.ID), TurnState: resolved.UpstreamTurnState}
	return s.contentFree(ctx, options, account, controlModel(request.Path), "codex_control", request.Body, target, func() (CodexOperationResult, error) {
		return s.provider.Control(ctx, target, request)
	})
}

func (s *CodexOperations) CreateFile(ctx context.Context, keyID, sessionID string, body json.RawMessage) (CodexOperationResult, error) {
	return s.CreateFileWithOptions(ctx, CodexOperationOptions{KeyID: keyID, SessionID: sessionID}, body)
}

func (s *CodexOperations) CreateFileWithOptions(ctx context.Context, options CodexOperationOptions, body json.RawMessage) (CodexOperationResult, error) {
	keyID := options.KeyID
	if _, err := s.activeKey(ctx, keyID); err != nil {
		return CodexOperationResult{}, err
	}
	var request struct {
		FileName string `json:"file_name"`
		FileSize int64  `json:"file_size"`
		UseCase  string `json:"use_case"`
	}
	if err := decodeExactObject(body, &request); err != nil || strings.TrimSpace(request.FileName) == "" ||
		request.FileSize <= 0 || request.FileSize > 512<<20 {
		return CodexOperationResult{}, &ProxyError{Code: "invalid_request", Status: 400, Message: "Invalid file registration"}
	}
	resolved, err := s.resolveConversationOwner(ctx, options)
	if err != nil {
		return CodexOperationResult{}, err
	}
	account, err := s.accountForOwner(ctx, keyID, resolved.Owner, nil)
	if err != nil {
		return CodexOperationResult{}, err
	}
	target := CodexOperationTarget{Account: account, KeyID: keyID,
		SessionID: options.upstreamSessionID(account.ID), TurnState: resolved.UpstreamTurnState}
	return s.contentFree(ctx, options, account, "files-create", "files_create", body, target, func() (CodexOperationResult, error) {
		result, err := s.provider.CreateFile(ctx, target, body)
		if err != nil || result.Failed || result.Status < 200 || result.Status >= 300 {
			return result, err
		}
		var response struct {
			FileID string `json:"file_id"`
		}
		if json.Unmarshal(result.Body, &response) != nil || strings.TrimSpace(response.FileID) == "" {
			return CodexOperationResult{}, &ProviderFailure{Code: "invalid_upstream_response", Status: 502, Dispatched: true}
		}
		ownerRecord := CodexResourceOwner{
			ResourceType: CodexResourceFile, ResourceID: response.FileID, KeyID: keyID,
			AccountID: account.ID, AccountGeneration: account.Generation, RouteRevision: account.RouteRevision, ExpiresAt: s.now().Add(s.ttl),
		}
		if err := s.owners.SaveCodexResourceOwner(ctx, ownerRecord); err != nil {
			return CodexOperationResult{}, &ProxyError{Code: "file_owner_persistence_failed", Status: 503, Message: "File owner could not be persisted"}
		}
		return result, nil
	})
}

func (s *CodexOperations) FinalizeFile(ctx context.Context, keyID, fileID string) (CodexOperationResult, error) {
	return s.FinalizeFileWithOptions(ctx, CodexOperationOptions{KeyID: keyID}, fileID)
}

func (s *CodexOperations) FinalizeFileWithOptions(ctx context.Context, options CodexOperationOptions, fileID string) (CodexOperationResult, error) {
	keyID := options.KeyID
	if fileID == "" || len(fileID) > 512 {
		return CodexOperationResult{}, &ProxyError{Code: "invalid_request", Status: 400, Message: "Invalid file ID"}
	}
	if _, err := s.activeKey(ctx, keyID); err != nil {
		return CodexOperationResult{}, err
	}
	owner, err := s.owners.GetCodexResourceOwner(ctx, CodexResourceFile, fileID, keyID, s.now())
	if errors.Is(err, domain.ErrNotFound) || err == nil && owner.AccountID == "" {
		return CodexOperationResult{}, &ProxyError{Code: "file_owner_not_found", Status: 409, Message: "File owner is unavailable; recreate the file on a permitted account"}
	}
	if err != nil {
		return CodexOperationResult{}, err
	}
	resolved, err := resolveConversation(ctx, s.store, keyID, "", options.identity(), s.now())
	if err != nil {
		return CodexOperationResult{}, err
	}
	if err := resolved.validateFileOwner(owner.AccountID, owner.AccountGeneration, owner.RouteRevision); err != nil {
		return CodexOperationResult{}, err
	}
	account, err := s.strictOwnerAccount(ctx, keyID, owner.AccountID, owner.AccountGeneration, owner.RouteRevision)
	if err != nil {
		return CodexOperationResult{}, err
	}
	return s.contentFree(ctx, options, account, "files-finalize", "files_finalize", []byte(`{}`), CodexOperationTarget{Account: account, KeyID: keyID}, func() (CodexOperationResult, error) {
		result, err := s.provider.FinalizeFile(ctx, CodexOperationTarget{Account: account, KeyID: keyID}, fileID)
		if err != nil {
			return result, err
		}
		var response struct {
			Status string `json:"status"`
		}
		if result.Status == 200 && json.Unmarshal(result.Body, &response) == nil && response.Status == "success" {
			owner.ExpiresAt = s.now().Add(s.ttl)
			if err := s.owners.SaveCodexResourceOwner(ctx, owner); err != nil {
				return CodexOperationResult{}, &ProxyError{Code: "file_owner_persistence_failed", Status: 503, Message: "File owner could not be persisted"}
			}
		}
		return result, nil
	})
}

func (s *CodexOperations) Transcribe(ctx context.Context, keyID, sessionID string, request CodexTranscriptionRequest) (CodexOperationResult, error) {
	return s.TranscribeWithOptions(ctx, CodexOperationOptions{KeyID: keyID, SessionID: sessionID}, request)
}

func (s *CodexOperations) TranscribeWithOptions(ctx context.Context, options CodexOperationOptions, request CodexTranscriptionRequest) (CodexOperationResult, error) {
	keyID := options.KeyID
	key, err := s.activeKey(ctx, keyID)
	if err != nil {
		return CodexOperationResult{}, err
	}
	model := strings.TrimSpace(request.Model)
	if model == "" {
		model = transcriptionModel
	}
	if key.EnforcedModel != nil {
		model = *key.EnforcedModel
	}
	if len(key.AllowedModels) != 0 && !modelAllowed(key.AllowedModels, model) {
		return CodexOperationResult{}, &ProxyError{Code: "model_not_allowed", Status: 403, Message: "This key does not allow the requested transcription model"}
	}
	request.Model = model
	resolved, err := s.resolveConversationOwner(ctx, options)
	if err != nil {
		return CodexOperationResult{}, err
	}
	owner := resolved.Owner
	var account domain.Account
	var sourceModel domain.ModelSourceModel
	if owner != nil {
		account, err = s.accountForOwner(ctx, keyID, owner, nil)
		if err == nil && account.Kind == domain.AccountExternal {
			var source domain.ModelSource
			source, err = s.store.GetModelSource(ctx, account.ID)
			if err == nil {
				sourceModel, _ = source.Model(model)
			}
		}
	} else {
		account, sourceModel, err = s.selectTranscriptionAccount(ctx, keyID, model)
	}
	if err != nil {
		return CodexOperationResult{}, err
	}
	target := CodexOperationTarget{Account: account, KeyID: keyID,
		SessionID: options.upstreamSessionID(account.ID), TurnState: resolved.UpstreamTurnState}
	return s.billed(ctx, options, key, account, model, "transcription", target, func() (CodexOperationResult, error) {
		return s.provider.Transcribe(ctx, target, request)
	}, &sourceModel, request.Audio, false)
}

func validateControlRequest(request CodexControlRequest) error {
	allowed := map[string]map[string]bool{
		"thread/goal/get":          {"GET": true, "POST": true},
		"thread/goal/set":          {"POST": true},
		"thread/goal/clear":        {"POST": true},
		"analytics-events/events":  {"POST": true},
		"memories/trace_summarize": {"POST": true},
		"safety/arc":               {"POST": true},
		"alpha/search":             {"POST": true},
		"agent-identities/jwks":    {"GET": true},
	}
	if request.Method == "" || !allowed[request.Path][request.Method] || len(request.Body) > 2<<20 {
		return &ProxyError{Code: "invalid_request", Status: 400, Message: "Unsupported Codex control route"}
	}
	return validateControlQuery(request.Query)
}

func validateControlQuery(query [][2]string) error {
	if len(query) > 32 {
		return &ProxyError{Code: "invalid_request", Status: 400, Message: "Too many Codex control query values"}
	}
	for _, pair := range query {
		if len(pair[0]) > 512 || len(pair[1]) > 512 {
			return &ProxyError{Code: "invalid_request", Status: 400, Message: "Unsupported Codex control query"}
		}
	}
	return nil
}

func modelAllowed(allowed []string, model string) bool {
	canonical := canonicalModel(model)
	for _, candidate := range allowed {
		if canonicalModel(candidate) == canonical {
			return true
		}
	}
	return false
}

func isFastTier(tier string) bool {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "priority", "fast", "ultrafast":
		return true
	default:
		return false
	}
}

func decodeExactObject(body json.RawMessage, target any) error {
	var object map[string]json.RawMessage
	if json.Unmarshal(body, &object) != nil || object == nil {
		return errors.New("not an object")
	}
	return json.Unmarshal(body, target)
}

func collectFileIDs(value json.RawMessage) []string {
	var parsed any
	if json.Unmarshal(value, &parsed) != nil {
		return nil
	}
	seen := map[string]bool{}
	var result []string
	var visit func(any)
	visit = func(item any) {
		switch item := item.(type) {
		case map[string]any:
			for key, child := range item {
				if key == "file_id" {
					if id, ok := child.(string); ok && id != "" && !seen[id] {
						seen[id] = true
						result = append(result, id)
					}
				}
				if key == "file_ids" {
					if ids, ok := child.([]any); ok {
						for _, idValue := range ids {
							if id, ok := idValue.(string); ok && id != "" && !seen[id] {
								seen[id] = true
								result = append(result, id)
							}
						}
					}
				}
				visit(child)
			}
		case []any:
			for _, child := range item {
				visit(child)
			}
		}
	}
	visit(parsed)
	return result
}
