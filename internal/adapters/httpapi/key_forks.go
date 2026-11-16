package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

// KeyForksStore authenticates the parent key and stores its forks.
type KeyForksStore interface {
	FindAPIKeyByHash(context.Context, string) (domain.APIKey, error)
	application.KeyForkStore
}

// KeyForksHandler serves /v1/key/forks: the holder of a key manages forks of
// it with that key as the bearer.
type KeyForksHandler struct {
	store  KeyForksStore
	forks  *application.KeyForks
	logger *slog.Logger
}

func NewKeyForksHandler(store KeyForksStore, logger *slog.Logger) *KeyForksHandler {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &KeyForksHandler{store: store, forks: application.NewKeyForks(store, nil), logger: logger}
}

func (h *KeyForksHandler) RegisterRoutes(mux *http.ServeMux) {
	for _, path := range []string{"/v1/key/forks", "/v1/key/forks/{$}"} {
		mux.HandleFunc("GET "+path, h.list)
		mux.HandleFunc("POST "+path, h.create)
	}
	mux.HandleFunc("GET /v1/key/forks/{id}", h.get)
	mux.HandleFunc("PATCH /v1/key/forks/{id}", h.update)
	mux.HandleFunc("DELETE /v1/key/forks/{id}", h.delete)
}

// forkView is a fork as the key API shows it: its own settings, budget
// limits with their current values, its pool cap and who wrote it.
type forkView struct {
	ID                      string              `json:"id"`
	Object                  string              `json:"object"`
	Name                    string              `json:"name"`
	KeyPrefix               string              `json:"keyPrefix"`
	IsActive                bool                `json:"isActive"`
	ExpiresAt               *time.Time          `json:"expiresAt"`
	CreatedAt               time.Time           `json:"createdAt"`
	LastUsedAt              *time.Time          `json:"lastUsedAt"`
	CreatedBy               domain.KeyActor     `json:"createdBy"`
	UpdatedBy               domain.KeyActor     `json:"updatedBy"`
	UpdatedAt               time.Time           `json:"updatedAt"`
	AllowedModels           []string            `json:"allowedModels"`
	EnforcedModel           *string             `json:"enforcedModel"`
	AllowedReasoningEfforts []string            `json:"allowedReasoningEfforts"`
	EnforcedReasoningEffort *string             `json:"enforcedReasoningEffort"`
	EnforcedServiceTier     *string             `json:"enforcedServiceTier"`
	Limits                  []domain.LimitRule  `json:"limits"`
	RequestPool             *domain.RequestPool `json:"requestPool"`
	Key                     string              `json:"key,omitempty"`
}

func viewFork(fork domain.APIKey) forkView {
	view := forkView{ID: fork.ID, Object: "api_key.fork", Name: fork.Name, KeyPrefix: fork.KeyPrefix, IsActive: fork.IsActive,
		ExpiresAt: fork.ExpiresAt, CreatedAt: fork.CreatedAt, LastUsedAt: fork.LastUsedAt,
		AllowedModels: fork.AllowedModels, EnforcedModel: fork.EnforcedModel, AllowedReasoningEfforts: fork.AllowedReasoningEfforts,
		EnforcedReasoningEffort: fork.EnforcedReasoningEffort, EnforcedServiceTier: fork.EnforcedServiceTier,
		Limits: fork.Limits, RequestPool: fork.RequestPool}
	if view.Limits == nil {
		view.Limits = []domain.LimitRule{}
	}
	if fork.Fork != nil {
		view.CreatedBy, view.UpdatedBy, view.UpdatedAt = fork.Fork.CreatedBy, fork.Fork.UpdatedBy, fork.Fork.UpdatedAt
	}
	return view
}

// forkPayload holds the fields a create or update sends; an absent field is
// kept and null clears a nullable one.
type forkPayload struct {
	Name                    *string         `json:"name"`
	IsActive                *bool           `json:"isActive"`
	ExpiresAt               json.RawMessage `json:"expiresAt"`
	AllowedModels           json.RawMessage `json:"allowedModels"`
	EnforcedModel           json.RawMessage `json:"enforcedModel"`
	AllowedReasoningEfforts json.RawMessage `json:"allowedReasoningEfforts"`
	EnforcedReasoningEffort json.RawMessage `json:"enforcedReasoningEffort"`
	EnforcedServiceTier     json.RawMessage `json:"enforcedServiceTier"`
	Limits                  json.RawMessage `json:"limits"`
	RequestPool             json.RawMessage `json:"requestPool"`
}

func invalidFork(param, message string) error {
	return &application.ProxyError{Code: "invalid_request", Status: 422, Message: message, Param: param}
}

// apply sets the present fields on fork.
func (p forkPayload) apply(fork *domain.APIKey) error {
	if p.Name != nil {
		fork.Name = *p.Name
	}
	if p.IsActive != nil {
		fork.IsActive = *p.IsActive
	}
	nullable := func(raw json.RawMessage, dst any, param string) error {
		if raw == nil {
			return nil
		}
		if strictUnmarshal(raw, dst) != nil {
			return invalidFork(param, "Invalid "+param)
		}
		return nil
	}
	for _, field := range []struct {
		raw   json.RawMessage
		dst   any
		param string
	}{
		{p.ExpiresAt, &fork.ExpiresAt, "expiresAt"}, {p.AllowedModels, &fork.AllowedModels, "allowedModels"},
		{p.EnforcedModel, &fork.EnforcedModel, "enforcedModel"}, {p.AllowedReasoningEfforts, &fork.AllowedReasoningEfforts, "allowedReasoningEfforts"},
		{p.EnforcedReasoningEffort, &fork.EnforcedReasoningEffort, "enforcedReasoningEffort"}, {p.EnforcedServiceTier, &fork.EnforcedServiceTier, "enforcedServiceTier"},
	} {
		if err := nullable(field.raw, field.dst, field.param); err != nil {
			return err
		}
	}
	if len(fork.AllowedModels) == 0 {
		fork.AllowedModels = nil // no list allows every model the parent allows
	}
	if fork.AllowedReasoningEfforts != nil && len(fork.AllowedReasoningEfforts) == 0 {
		return invalidFork("allowedReasoningEfforts", "allowedReasoningEfforts needs at least one effort, or null")
	}
	if p.Limits != nil {
		var limits []domain.LimitRule
		if strictUnmarshal(p.Limits, &limits) != nil || limits == nil {
			return invalidFork("limits", "Invalid limits")
		}
		for i := range limits {
			limits[i].ID, limits[i].CurrentValue, limits[i].ResetAt = 0, 0, time.Time{}
		}
		if domain.ValidateLimitRules(limits) != nil {
			return invalidFork("limits", "Invalid limits")
		}
		fork.Limits = limits
	}
	if p.RequestPool != nil {
		pool, err := decodeRequestPool(p.RequestPool)
		if err != nil {
			return invalidFork("requestPool", err.Error())
		}
		fork.RequestPool = pool
	}
	return nil
}

func strictUnmarshal(raw json.RawMessage, dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(dst)
}

func (h *KeyForksHandler) parent(w http.ResponseWriter, r *http.Request) (domain.APIKey, bool) {
	key, err := authenticateBearerKey(r, h.store)
	if err != nil {
		writeKeyForkError(w, err)
		return domain.APIKey{}, false
	}
	return key, true
}

func (h *KeyForksHandler) list(w http.ResponseWriter, r *http.Request) {
	parent, ok := h.parent(w, r)
	if !ok {
		return
	}
	forks, err := h.forks.List(r.Context(), parent)
	if err != nil {
		writeKeyForkError(w, err)
		return
	}
	views := make([]forkView, 0, len(forks))
	for _, fork := range forks {
		views = append(views, viewFork(fork))
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": views})
}

func (h *KeyForksHandler) get(w http.ResponseWriter, r *http.Request) {
	parent, ok := h.parent(w, r)
	if !ok {
		return
	}
	fork, err := h.forks.Get(r.Context(), parent, r.PathValue("id"))
	if err != nil {
		writeKeyForkError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, viewFork(fork))
}

func (h *KeyForksHandler) create(w http.ResponseWriter, r *http.Request) {
	parent, ok := h.parent(w, r)
	if !ok {
		return
	}
	var payload forkPayload
	if !decodeJSON(w, r, &payload) {
		return
	}
	if payload.Name == nil {
		writeKeyForkError(w, invalidFork("name", "A fork needs a name"))
		return
	}
	plain, hash, prefix := issueForkKey()
	fork := domain.APIKey{ID: newID("key_"), KeyHash: hash, KeyPrefix: prefix, IsActive: true}
	if err := payload.apply(&fork); err != nil {
		writeKeyForkError(w, err)
		return
	}
	created, err := h.forks.Create(r.Context(), parent, fork)
	if err != nil {
		writeKeyForkError(w, err)
		return
	}
	h.logger.Info("api key fork created", "parent_key_id", parent.ID, "fork_key_id", created.ID, "actor", domain.KeyActorOwner)
	view := viewFork(created)
	view.Key = plain
	writeJSON(w, http.StatusCreated, view)
}

func (h *KeyForksHandler) update(w http.ResponseWriter, r *http.Request) {
	parent, ok := h.parent(w, r)
	if !ok {
		return
	}
	var payload forkPayload
	if !decodeJSON(w, r, &payload) {
		return
	}
	updated, err := h.forks.Update(r.Context(), parent, r.PathValue("id"), payload.apply)
	if err != nil {
		writeKeyForkError(w, err)
		return
	}
	h.logger.Info("api key fork updated", "parent_key_id", parent.ID, "fork_key_id", updated.ID, "actor", domain.KeyActorOwner)
	writeJSON(w, http.StatusOK, viewFork(updated))
}

func (h *KeyForksHandler) delete(w http.ResponseWriter, r *http.Request) {
	parent, ok := h.parent(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if err := h.forks.Delete(r.Context(), parent, id); err != nil {
		writeKeyForkError(w, err)
		return
	}
	h.logger.Info("api key fork deleted", "parent_key_id", parent.ID, "fork_key_id", id, "actor", domain.KeyActorOwner)
	w.WriteHeader(http.StatusNoContent)
}

// writeKeyForkError answers in the OpenAI error shape; a fork wider than its
// parent names the field in param.
func writeKeyForkError(w http.ResponseWriter, err error) {
	var exceeds *domain.ForkExceedsParent
	switch {
	case errors.As(err, &exceeds):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": map[string]any{
			"type": "invalid_request_error", "code": "fork_exceeds_parent", "param": exceeds.Field,
			"message": "The fork's " + exceeds.Field + " exceeds what its parent key allows",
		}})
	case errors.Is(err, domain.ErrForkLimitReached):
		writeError(w, http.StatusConflict, "fork_limit_reached", "A key has at most 100 forks")
	case errors.Is(err, domain.ErrNotFound):
		writeProxyError(w, &application.ProxyError{Code: "fork_not_found", Status: 404, Message: "This key has no such fork"})
	case errors.Is(err, domain.ErrInvalid):
		writeProxyError(w, &application.ProxyError{Code: "invalid_request", Status: 422, Message: "Invalid fork settings"})
	default:
		writeProxyError(w, err)
	}
}
