package domain

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"
)

type ModelSourceKind string

const (
	ModelSourceOpenAICompatible ModelSourceKind = "openai_compatible"
	ModelSourceZAI              ModelSourceKind = "zai"
)

type ModelSource struct {
	ID             string                    `json:"id"`
	Name           string                    `json:"name"`
	Kind           ModelSourceKind           `json:"kind"`
	BaseURL        string                    `json:"baseUrl"`
	Enabled        bool                      `json:"isEnabled"`
	Health         string                    `json:"healthStatus"`
	Chat           bool                      `json:"supportsChatCompletions"`
	Responses      bool                      `json:"supportsResponses"`
	Audio          bool                      `json:"supportsAudioTranscriptions"`
	Embeddings     bool                      `json:"supportsEmbeddings"`
	TimeoutSeconds int                       `json:"timeoutSeconds,omitempty"`
	MaxConcurrency int                       `json:"maxConcurrency,omitempty"`
	ProviderConfig ModelSourceProviderConfig `json:"providerConfig,omitempty"`
	Models         []ModelSourceModel        `json:"models"`
	CreatedAt      time.Time                 `json:"createdAt"`
	UpdatedAt      time.Time                 `json:"updatedAt"`
}

type ModelSourceProviderConfig struct {
	ExtraBody          map[string]json.RawMessage `json:"extraBody,omitempty"`
	DropBodyFields     []string                   `json:"dropBodyFields,omitempty"`
	AllowedHostedTools []string                   `json:"allowedHostedTools,omitempty"`
	EnableGLMThinking  bool                       `json:"enableGlmThinking,omitempty"`
	AllowNoSSEDone     bool                       `json:"allowNoSseDone,omitempty"`
	AllowMissingUsage  bool                       `json:"allowMissingUsage,omitempty"`
}

type ModelSourceModel struct {
	ID               int64     `json:"id,omitempty"`
	SourceID         string    `json:"sourceId"`
	Model            string    `json:"model"`
	Aliases          []string  `json:"aliases,omitempty"`
	UpstreamModel    string    `json:"upstreamModel,omitempty"`
	DisplayName      string    `json:"displayName,omitempty"`
	ContextWindow    int64     `json:"contextWindow,omitempty"`
	MaxOutputTokens  int64     `json:"maxOutputTokens,omitempty"`
	Streaming        bool      `json:"supportsStreaming"`
	Tools            bool      `json:"supportsTools"`
	Vision           bool      `json:"supportsVision"`
	InputPerMillion  *float64  `json:"inputPer1M"`
	CachedPerMillion *float64  `json:"cachedInputPer1M"`
	OutputPerMillion *float64  `json:"outputPer1M"`
	AudioPerMinute   *float64  `json:"audioPerMinute"`
	RawMetadataJSON  string    `json:"rawMetadataJson,omitempty"`
	Enabled          bool      `json:"isEnabled"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

func (source ModelSource) Validate() error {
	source.Name = strings.TrimSpace(source.Name)
	if source.ID == "" || source.Name == "" || len(source.Name) > 128 {
		return fmt.Errorf("%w: model source id and name are required", ErrInvalid)
	}
	switch source.Kind {
	case ModelSourceOpenAICompatible:
	case ModelSourceZAI:
		if source.Audio || source.Embeddings {
			return fmt.Errorf("%w: Z.AI sources support Chat Completions and Responses only", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: unsupported model source kind", ErrInvalid)
	}
	if !source.Chat && !source.Responses && !source.Audio && !source.Embeddings {
		return fmt.Errorf("%w: model source needs at least one protocol", ErrInvalid)
	}
	parsed, err := url.Parse(strings.TrimSpace(source.BaseURL))
	if err != nil || parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.Host == "" ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return fmt.Errorf("%w: model source base URL must be absolute HTTP(S)", ErrInvalid)
	}
	if source.TimeoutSeconds < 0 || source.TimeoutSeconds > 86400 || source.MaxConcurrency < 0 || source.MaxConcurrency > 100000 {
		return fmt.Errorf("%w: invalid model source timeout or concurrency", ErrInvalid)
	}
	if len(source.Models) == 0 {
		return fmt.Errorf("%w: model source requires at least one model", ErrInvalid)
	}
	seen := make(map[string]bool)
	for i := range source.Models {
		model := &source.Models[i]
		model.SourceID = source.ID
		if err := model.Validate(); err != nil {
			return err
		}
		for _, name := range append([]string{model.Model}, model.Aliases...) {
			name = strings.TrimSpace(name)
			if name == "" || seen[name] {
				return fmt.Errorf("%w: duplicate or empty model mapping", ErrInvalid)
			}
			seen[name] = true
		}
	}
	return validateProviderConfig(source.ProviderConfig)
}

func (model ModelSourceModel) Validate() error {
	model.Model = strings.TrimSpace(model.Model)
	if model.Model == "" || len(model.Model) > 255 {
		return fmt.Errorf("%w: model source model is required", ErrInvalid)
	}
	if model.DisplayName != "" && len(model.DisplayName) > 255 {
		return fmt.Errorf("%w: model display name is too long", ErrInvalid)
	}
	if model.ContextWindow < 0 || model.MaxOutputTokens < 0 {
		return fmt.Errorf("%w: model context and output limits must be positive", ErrInvalid)
	}
	for _, value := range []*float64{model.InputPerMillion, model.CachedPerMillion, model.OutputPerMillion, model.AudioPerMinute} {
		if value != nil && (*value < 0 || math.IsNaN(*value) || math.IsInf(*value, 0) || *value > 1_000_000) {
			return fmt.Errorf("%w: model pricing must be finite and non-negative", ErrInvalid)
		}
	}
	if model.UpstreamModel != "" && (strings.TrimSpace(model.UpstreamModel) == "" || len(model.UpstreamModel) > 255) {
		return fmt.Errorf("%w: upstream model mapping is invalid", ErrInvalid)
	}
	if model.RawMetadataJSON != "" {
		var object map[string]json.RawMessage
		if json.Unmarshal([]byte(model.RawMetadataJSON), &object) != nil || object == nil {
			return fmt.Errorf("%w: raw model metadata must be a JSON object", ErrInvalid)
		}
	}
	return nil
}

func validateProviderConfig(config ModelSourceProviderConfig) error {
	for field, value := range config.ExtraBody {
		if strings.TrimSpace(field) == "" || !json.Valid(value) {
			return fmt.Errorf("%w: provider extra body is invalid", ErrInvalid)
		}
	}
	for _, field := range append(config.DropBodyFields, config.AllowedHostedTools...) {
		if strings.TrimSpace(field) == "" {
			return fmt.Errorf("%w: provider field lists cannot contain blanks", ErrInvalid)
		}
	}
	return nil
}

func (source ModelSource) Account() Account {
	return Account{
		ID: source.ID, Kind: AccountExternal, Provider: string(source.Kind), BaseURL: source.BaseURL,
		Email: source.ID, Alias: source.Name, DisplayName: source.Name, PlanType: "external",
		RoutingPolicy: "normal", Status: AccountActive, CreatedAt: source.CreatedAt,
		RequiresEgressDecision: true,
	}
}

func (model ModelSourceModel) Matches(requestModel string) bool {
	requestModel = strings.TrimSpace(requestModel)
	if requestModel == model.Model {
		return true
	}
	for _, alias := range model.Aliases {
		if strings.TrimSpace(alias) == requestModel {
			return true
		}
	}
	return false
}

func (source ModelSource) Model(requestModel string) (ModelSourceModel, bool) {
	for _, model := range source.Models {
		if model.Enabled && model.Matches(requestModel) {
			return model, true
		}
	}
	return ModelSourceModel{}, false
}
