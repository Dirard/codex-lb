package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
	"codex-lb/internal/domain/pricing"
)

type ModelPricingHandler struct {
	service *application.ModelPricingService
}

func NewModelPricingHandler(store application.ModelPricingStore) *ModelPricingHandler {
	return &ModelPricingHandler{service: application.NewModelPricingService(store, nil)}
}

// RegisterAdminRoutes must be called on the existing requireAdmin-wrapped mux.
func (h *ModelPricingHandler) RegisterAdminRoutes(mux *http.ServeMux) {
	for _, path := range []string{"/api/model-prices", "/api/model-prices/{$}"} {
		mux.HandleFunc("GET "+path, h.list)
	}
	mux.HandleFunc("PUT /api/model-prices/{model...}", h.save)
	mux.HandleFunc("DELETE /api/model-prices/{model...}", h.delete)
}

func (h *ModelPricingHandler) list(w http.ResponseWriter, r *http.Request) {
	result, err := h.service.List(r.Context())
	if err != nil {
		modelPricingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *ModelPricingHandler) save(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Price *modelPriceInput `json:"price"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	if request.Price == nil {
		modelPricingError(w, domain.ErrInvalid)
		return
	}
	result, err := h.service.Save(r.Context(), r.PathValue("model"), request.Price.Price)
	if err != nil {
		modelPricingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

type modelPriceInput struct{ pricing.Price }

// Omitted rates must not silently turn a tariff (and its history) into zero.
// Explicit zero rates remain valid; the domain validates numeric bounds.
func (p *modelPriceInput) UnmarshalJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&p.Price); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	for _, tier := range []string{"standard", "priority", "flex", "longContext"} {
		encoded, exists := fields[tier]
		if !exists && tier != "standard" {
			continue
		}
		var rates map[string]json.RawMessage
		if json.Unmarshal(encoded, &rates) != nil {
			return domain.ErrInvalid
		}
		for _, name := range []string{"inputMicrodollarsPerMillion", "cachedMicrodollarsPerMillion", "outputMicrodollarsPerMillion"} {
			value, exists := rates[name]
			if !exists || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return domain.ErrInvalid
			}
		}
	}
	return nil
}

func (h *ModelPricingHandler) delete(w http.ResponseWriter, r *http.Request) {
	summary, err := h.service.Delete(r.Context(), r.PathValue("model"))
	if err != nil {
		modelPricingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Reprice application.ModelRepriceSummary `json:"reprice"`
	}{summary})
}

func modelPricingError(w http.ResponseWriter, err error) {
	if errors.Is(err, domain.ErrConflict) {
		writeError(w, http.StatusConflict, "price_reconciliation_required", "Current cost-limit charges cannot be reconciled safely")
		return
	}
	if errors.Is(err, domain.ErrInvalid) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_model_price", "Invalid model ID or price")
		return
	}
	writeError(w, http.StatusInternalServerError, "model_price_error", "Model prices could not be loaded or changed")
}
