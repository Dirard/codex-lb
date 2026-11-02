package application

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"codex-lb/internal/domain"
	"codex-lb/internal/domain/pricing"
)

type ModelSourceRepository interface {
	ListModelSources(context.Context) ([]domain.ModelSource, error)
	GetModelSource(context.Context, string) (domain.ModelSource, error)
	// SaveModelSource atomically upserts the source, its models, and the
	// matching external Account row; credential may be nil to keep stored bytes.
	SaveModelSource(context.Context, domain.ModelSource, *domain.AccountCredential) error
	DeleteModelSource(context.Context, string) error
}

type ModelSourceModelInput struct {
	Model            string   `json:"model"`
	Aliases          []string `json:"aliases"`
	UpstreamModel    *string  `json:"upstreamModel"`
	DisplayName      *string  `json:"displayName"`
	ContextWindow    *int64   `json:"contextWindow"`
	MaxOutputTokens  *int64   `json:"maxOutputTokens"`
	Streaming        *bool    `json:"supportsStreaming"`
	Tools            *bool    `json:"supportsTools"`
	Vision           *bool    `json:"supportsVision"`
	InputPerMillion  *float64 `json:"inputPer1M"`
	CachedPerMillion *float64 `json:"cachedInputPer1M"`
	OutputPerMillion *float64 `json:"outputPer1M"`
	AudioPerMinute   *float64 `json:"audioPerMinute"`
	RawMetadataJSON  *string  `json:"rawMetadataJson"`
	Enabled          *bool    `json:"isEnabled"`
}

type ModelSourceCreateRequest struct {
	Name           string                            `json:"name"`
	BaseURL        string                            `json:"baseUrl"`
	APIKey         NullableString                    `json:"apiKey"`
	Kind           domain.ModelSourceKind            `json:"kind"`
	Chat           *bool                             `json:"supportsChatCompletions"`
	Responses      *bool                             `json:"supportsResponses"`
	Audio          *bool                             `json:"supportsAudioTranscriptions"`
	Embeddings     *bool                             `json:"supportsEmbeddings"`
	Timeout        *int                              `json:"timeoutSeconds"`
	Concurrency    *int                              `json:"maxConcurrency"`
	ProviderConfig *domain.ModelSourceProviderConfig `json:"providerConfig"`
	Models         []ModelSourceModelInput           `json:"models"`
}

type ModelSourceUpdateRequest struct {
	Name           *string                           `json:"name"`
	BaseURL        *string                           `json:"baseUrl"`
	APIKey         NullableString                    `json:"apiKey"`
	Enabled        *bool                             `json:"isEnabled"`
	Chat           *bool                             `json:"supportsChatCompletions"`
	Responses      *bool                             `json:"supportsResponses"`
	Audio          *bool                             `json:"supportsAudioTranscriptions"`
	Embeddings     *bool                             `json:"supportsEmbeddings"`
	Timeout        NullableInt                       `json:"timeoutSeconds"`
	Concurrency    NullableInt                       `json:"maxConcurrency"`
	ProviderConfig *domain.ModelSourceProviderConfig `json:"providerConfig"`
	Models         *[]ModelSourceModelInput          `json:"models"`
}

// NullableInt distinguishes an omitted patch field from an explicit reset to
// the source's default timeout or concurrency policy.
type NullableInt struct {
	Set   bool
	Value *int
}

func (value *NullableInt) UnmarshalJSON(raw []byte) error {
	value.Set = true
	return json.Unmarshal(raw, &value.Value)
}

type NullableString struct {
	Set   bool
	Value *string
}

func (value *NullableString) UnmarshalJSON(raw []byte) error {
	value.Set = true
	if string(raw) == "null" {
		value.Value = nil
		return nil
	}
	return json.Unmarshal(raw, &value.Value)
}

type ModelSourceService struct {
	store  ModelSourceRepository
	cipher SecretCipher
	// AllowPrivateDestinations accepts base URLs on non-public addresses for
	// self-hosted sources; otherwise such URLs are rejected when set.
	AllowPrivateDestinations bool
	// Grok is the Grok pool that subscription sources borrow tokens from;
	// without it such sources cannot be created.
	Grok GrokSourceAccounts
}

// GrokSourceAccounts is the Grok pool as model source creation sees it.
type GrokSourceAccounts interface {
	GrokAccountExists(ctx context.Context, accountID string) (bool, error)
	GrokTextModels(ctx context.Context, accountID string) ([]GrokTextModel, error)
}

// grokContextWindow is the smallest context of the current Grok text models
// (grok-code-fast-1), so Codex compacts before any of them overflows.
const grokContextWindow = 256000

func NewModelSourceService(store ModelSourceRepository, cipher SecretCipher) *ModelSourceService {
	return &ModelSourceService{store: store, cipher: cipher}
}

func (s *ModelSourceService) List(ctx context.Context) ([]domain.ModelSource, error) {
	return s.store.ListModelSources(ctx)
}

func (s *ModelSourceService) Create(ctx context.Context, request ModelSourceCreateRequest) (domain.ModelSource, error) {
	now := time.Now().UTC()
	source := domain.ModelSource{
		ID: "src_" + rand.Text(), Name: request.Name, Kind: request.Kind, BaseURL: request.BaseURL,
		Enabled: true, Health: "unknown", Chat: boolOrDefault(request.Chat, true),
		Responses: boolValue(request.Responses), Audio: boolValue(request.Audio),
		Embeddings: boolValue(request.Embeddings), TimeoutSeconds: intOrDefault(request.Timeout),
		MaxConcurrency: intOrDefault(request.Concurrency), Models: modelsFromInputs(request.Models, nil),
		CreatedAt: now, UpdatedAt: now,
	}
	if request.ProviderConfig != nil {
		source.ProviderConfig = *request.ProviderConfig
	}
	if source.Kind == "" {
		source.Kind = domain.ModelSourceOpenAICompatible
	}
	if source.Kind == domain.ModelSourceGrokSubscription {
		if err := s.prepareGrokSource(ctx, &source, request); err != nil {
			return domain.ModelSource{}, err
		}
	}
	credential, err := s.credential(source.ID, request.APIKey, false)
	if err != nil {
		return domain.ModelSource{}, err
	}
	if credential == nil {
		credential = &domain.AccountCredential{AccountID: source.ID}
	}
	if err := source.Validate(); err != nil {
		return domain.ModelSource{}, err
	}
	if err := s.validateDestination(source.BaseURL); err != nil {
		return domain.ModelSource{}, err
	}
	if err := s.store.SaveModelSource(ctx, source, credential); err != nil {
		return domain.ModelSource{}, err
	}
	return s.store.GetModelSource(ctx, source.ID)
}

// validateDestination rejects base URLs that name this host or a non-public
// address literal. Names are checked again on every connection by the dial guard.
func (s *ModelSourceService) validateDestination(baseURL string) error {
	if s.AllowPrivateDestinations {
		return nil
	}
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return fmt.Errorf("%w: model source base URL must be absolute HTTP(S)", domain.ErrInvalid)
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return fmt.Errorf("%w: model source base URL must use a public address", domain.ErrInvalid)
	}
	if ip, err := netip.ParseAddr(host); err == nil && !domain.PublicUnicast(ip) {
		return fmt.Errorf("%w: model source base URL must use a public address", domain.ErrInvalid)
	}
	return nil
}

func (s *ModelSourceService) Update(ctx context.Context, id string, request ModelSourceUpdateRequest) (domain.ModelSource, error) {
	source, err := s.store.GetModelSource(ctx, id)
	if err != nil {
		return domain.ModelSource{}, err
	}
	if request.Name != nil {
		source.Name = *request.Name
	}
	if request.BaseURL != nil {
		source.BaseURL = *request.BaseURL
	}
	if request.Enabled != nil {
		source.Enabled = *request.Enabled
	}
	if request.Chat != nil {
		source.Chat = *request.Chat
	}
	if request.Responses != nil {
		source.Responses = *request.Responses
	}
	if request.Audio != nil {
		source.Audio = *request.Audio
	}
	if request.Embeddings != nil {
		source.Embeddings = *request.Embeddings
	}
	if request.Timeout.Set {
		source.TimeoutSeconds = intOrDefault(request.Timeout.Value)
	}
	if request.Concurrency.Set {
		source.MaxConcurrency = intOrDefault(request.Concurrency.Value)
	}
	if request.ProviderConfig != nil {
		source.ProviderConfig = *request.ProviderConfig
	}
	if request.Models != nil {
		source.Models = modelsFromInputs(*request.Models, source.Models)
	}
	if source.Kind == domain.ModelSourceGrokSubscription {
		if hasAPIKey(request.APIKey) {
			return domain.ModelSource{}, errGrokSourceKey
		}
		if request.ProviderConfig != nil {
			if err := s.requireGrokAccount(ctx, source.ProviderConfig.GrokAccountID); err != nil {
				return domain.ModelSource{}, err
			}
		}
	}
	source.UpdatedAt = time.Now().UTC()
	credential, err := s.credential(source.ID, request.APIKey, false)
	if err != nil {
		return domain.ModelSource{}, err
	}
	if err := source.Validate(); err != nil {
		return domain.ModelSource{}, err
	}
	if request.BaseURL != nil {
		if err := s.validateDestination(source.BaseURL); err != nil {
			return domain.ModelSource{}, err
		}
	}
	if err := s.store.SaveModelSource(ctx, source, credential); err != nil {
		return domain.ModelSource{}, err
	}
	return s.store.GetModelSource(ctx, id)
}

var errGrokSourceKey = fmt.Errorf("%w: Grok subscription sources use the account's token, not an API key", domain.ErrInvalid)

// prepareGrokSource checks the account link and fixed endpoint of a new Grok
// subscription source and, when no models are given, fills them from xAI.
func (s *ModelSourceService) prepareGrokSource(ctx context.Context, source *domain.ModelSource, request ModelSourceCreateRequest) error {
	if hasAPIKey(request.APIKey) {
		return errGrokSourceKey
	}
	if strings.TrimSpace(source.BaseURL) == "" {
		source.BaseURL = domain.GrokTextBaseURL
	}
	source.Chat = boolOrDefault(request.Chat, false)
	source.Responses = boolOrDefault(request.Responses, true)
	accountID := source.ProviderConfig.GrokAccountID
	if err := s.requireGrokAccount(ctx, accountID); err != nil {
		return err
	}
	if len(source.Models) != 0 {
		return nil
	}
	// Everything but the models is checked before xAI is asked for them.
	draft := *source
	draft.Models = []domain.ModelSourceModel{{Model: "pending", Enabled: true}}
	if err := draft.Validate(); err != nil {
		return err
	}
	listed, err := s.Grok.GrokTextModels(ctx, accountID)
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil || len(listed) == 0 {
		return &ProxyError{Code: "grok_models_unavailable", Status: 502,
			Message: "xAI did not list the Grok account's text models; check that the account is signed in"}
	}
	source.Models = grokSourceModels(listed, source.CreatedAt)
	return nil
}

func (s *ModelSourceService) requireGrokAccount(ctx context.Context, accountID string) error {
	if s.Grok == nil {
		return fmt.Errorf("%w: Grok accounts are not available", domain.ErrInvalid)
	}
	if strings.TrimSpace(accountID) == "" {
		return fmt.Errorf("%w: Grok subscription sources need a Grok account", domain.ErrInvalid)
	}
	exists, err := s.Grok.GrokAccountExists(ctx, accountID)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%w: unknown Grok account", domain.ErrInvalid)
	}
	return nil
}

// grokSourceModels turns xAI's list into enabled source models. An alias that
// repeats a model id or an earlier alias is dropped, as the source needs
// unique names.
func grokSourceModels(listed []GrokTextModel, now time.Time) []domain.ModelSourceModel {
	seen := make(map[string]bool, len(listed))
	for _, model := range listed {
		seen[model.ID] = true
	}
	models := make([]domain.ModelSourceModel, 0, len(listed))
	for _, model := range listed {
		aliases := make([]string, 0, len(model.Aliases))
		for _, alias := range model.Aliases {
			alias = strings.TrimSpace(alias)
			if alias != "" && !seen[alias] {
				seen[alias] = true
				aliases = append(aliases, alias)
			}
		}
		models = append(models, domain.ModelSourceModel{
			Model: model.ID, Aliases: aliases, DisplayName: model.ID, ContextWindow: grokContextWindow,
			Streaming: true, Tools: true, Vision: model.Vision,
			InputPerMillion: model.InputPerMillion, CachedPerMillion: model.CachedPerMillion, OutputPerMillion: model.OutputPerMillion,
			Enabled: true, CreatedAt: now, UpdatedAt: now,
		})
	}
	return models
}

func hasAPIKey(value NullableString) bool {
	return value.Set && value.Value != nil && strings.TrimSpace(*value.Value) != ""
}

func (s *ModelSourceService) Price(ctx context.Context, account domain.Account, model string) (pricing.Price, error) {
	if account.Kind != domain.AccountExternal {
		return pricing.Price{}, pricing.ErrUnpriced
	}
	source, err := s.store.GetModelSource(ctx, account.ID)
	if err != nil {
		return pricing.Price{}, err
	}
	entry, ok := source.Model(model)
	if !ok {
		return pricing.Price{}, pricing.ErrUnpriced
	}
	return ModelSourcePrice(entry)
}

func (s *ModelSourceService) Delete(ctx context.Context, id string) error {
	return s.store.DeleteModelSource(ctx, id)
}

func (s *ModelSourceService) credential(sourceID string, apiKey NullableString, _ bool) (*domain.AccountCredential, error) {
	if !apiKey.Set {
		return nil, nil
	}
	if apiKey.Value == nil {
		return &domain.AccountCredential{AccountID: sourceID}, nil
	}
	value := strings.TrimSpace(*apiKey.Value)
	if value == "" {
		return &domain.AccountCredential{AccountID: sourceID, ExternalKeyEncrypted: nil}, nil
	}
	encrypted, err := s.cipher.Encrypt([]byte(value))
	if err != nil {
		return nil, errors.New("could not encrypt model source credential")
	}
	return &domain.AccountCredential{AccountID: sourceID, ExternalKeyEncrypted: encrypted}, nil
}

func modelsFromInputs(inputs []ModelSourceModelInput, existing []domain.ModelSourceModel) []domain.ModelSourceModel {
	now := time.Now().UTC()
	old := make(map[string]domain.ModelSourceModel, len(existing))
	for _, model := range existing {
		old[model.Model] = model
	}
	models := make([]domain.ModelSourceModel, 0, len(inputs))
	for _, input := range inputs {
		previous := old[strings.TrimSpace(input.Model)]
		aliases := input.Aliases
		if aliases == nil {
			aliases = previous.Aliases
		}
		model := domain.ModelSourceModel{
			Model: input.Model, Aliases: aliases, UpstreamModel: pointerString(input.UpstreamModel, previous.UpstreamModel),
			DisplayName:     pointerString(input.DisplayName, previous.DisplayName),
			ContextWindow:   pointerInt64(input.ContextWindow, previous.ContextWindow),
			MaxOutputTokens: pointerInt64(input.MaxOutputTokens, previous.MaxOutputTokens),
			Streaming:       boolOrDefault(input.Streaming, previous.Streaming || previous.Model == ""),
			Tools:           boolOrDefault(input.Tools, previous.Tools), Vision: boolOrDefault(input.Vision, previous.Vision),
			InputPerMillion: input.InputPerMillion, CachedPerMillion: input.CachedPerMillion,
			OutputPerMillion: input.OutputPerMillion, AudioPerMinute: input.AudioPerMinute,
			RawMetadataJSON: pointerString(input.RawMetadataJSON, previous.RawMetadataJSON),
			Enabled:         boolOrDefault(input.Enabled, previous.Enabled || previous.Model == ""),
			CreatedAt:       previous.CreatedAt, UpdatedAt: now,
		}
		if model.CreatedAt.IsZero() {
			model.CreatedAt = now
		}
		models = append(models, model)
	}
	return models
}

func ModelSourcePrice(model domain.ModelSourceModel) (pricing.Price, error) {
	price := pricing.Price{Standard: pricing.Rates{
		Input: priceMicros(model.InputPerMillion), Cached: priceMicros(model.CachedPerMillion),
		Output: priceMicros(model.OutputPerMillion),
	}}
	if model.CachedPerMillion == nil && model.InputPerMillion != nil {
		price.Standard.Cached = price.Standard.Input
	}
	if err := price.Validate(); err != nil {
		return pricing.Price{}, err
	}
	return price, nil
}

func priceMicros(value *float64) int64 {
	if value == nil {
		return 0
	}
	return int64(math.Round(*value * 1_000_000))
}

func boolValue(value *bool) bool { return value != nil && *value }
func boolOrDefault(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}
func intOrDefault(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}
func pointerString(value *string, fallback string) string {
	if value == nil {
		return fallback
	}
	return *value
}
func pointerInt64(value *int64, fallback int64) int64 {
	if value == nil {
		return fallback
	}
	return *value
}
