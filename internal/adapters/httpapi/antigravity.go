package httpapi

import (
	"errors"
	"net/http"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

// ConfigureAntigravity supplies the Antigravity sign-in and token service
// before Handler is built; the same instance lends tokens to the provider.
func (s *Server) ConfigureAntigravity(service *application.AntigravityService) {
	s.antigravity = service
}

// registerAntigravityRoutes serves the Antigravity sign-in and model refresh
// of the Model sources page, behind the admin authentication.
func (s *Server) registerAntigravityRoutes(mux *http.ServeMux) {
	if s.antigravity == nil {
		return
	}
	mux.HandleFunc("POST /api/antigravity/sign-in", s.startAntigravitySignIn)
	mux.HandleFunc("POST /api/antigravity/sign-in/{flow}", s.completeAntigravitySignIn)
	mux.HandleFunc("POST /api/model-sources/{id}/antigravity/refresh-models", s.refreshAntigravityModels)
}

// antigravityFail answers with the service's own codes; tokens and provider
// bodies never reach these errors.
func (s *Server) antigravityFail(w http.ResponseWriter, err error) {
	var request *application.ProxyError
	switch {
	case errors.As(err, &request):
		writeProxyError(w, err)
	case errors.Is(err, domain.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Not found")
	default:
		s.fail(w, err)
	}
}

func (s *Server) startAntigravitySignIn(w http.ResponseWriter, r *http.Request) {
	var request struct {
		// SourceID names the source being re-authorized; empty adds an account.
		SourceID string `json:"sourceId"`
	}
	if r.ContentLength != 0 && !decodeJSON(w, r, &request) {
		return
	}
	start, err := s.antigravity.StartSignIn(r.Context(), request.SourceID)
	if err != nil {
		s.antigravityFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, start)
}

func (s *Server) completeAntigravitySignIn(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Code string `json:"code"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	source, err := s.antigravity.CompleteSignIn(r.Context(), r.PathValue("flow"), request.Code)
	if err != nil {
		s.antigravityFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, source)
}

func (s *Server) refreshAntigravityModels(w http.ResponseWriter, r *http.Request) {
	source, err := s.antigravity.RefreshModels(r.Context(), r.PathValue("id"))
	if err != nil {
		s.antigravityFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, source)
}
