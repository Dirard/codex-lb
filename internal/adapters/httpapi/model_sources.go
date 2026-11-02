package httpapi

import (
	"errors"
	"net/http"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

func (s *Server) registerModelSourceRoutes(mux *http.ServeMux) {
	for _, path := range []string{"/api/model-sources", "/api/model-sources/{$}"} {
		mux.HandleFunc("GET "+path, s.listModelSources)
		mux.HandleFunc("POST "+path, s.createModelSource)
	}
	mux.HandleFunc("PATCH /api/model-sources/{id}", s.updateModelSource)
	mux.HandleFunc("DELETE /api/model-sources/{id}", s.deleteModelSource)
}

func (s *Server) modelSources() (*application.ModelSourceService, bool) {
	store, ok := s.store.(application.ModelSourceRepository)
	if !ok {
		return nil, false
	}
	service := application.NewModelSourceService(store, s.cipher)
	service.AllowPrivateDestinations = s.config.AllowPrivateModelSources
	if s.video != nil {
		service.Grok = s.video
	}
	return service, true
}

// failModelSource answers a source edit; a Grok model list failure keeps its
// own code so the dashboard can explain it.
func (s *Server) failModelSource(w http.ResponseWriter, err error) {
	var proxyErr *application.ProxyError
	if errors.As(err, &proxyErr) {
		writeError(w, proxyErr.Status, proxyErr.Code, proxyErr.Message)
		return
	}
	s.fail(w, err)
}

func (s *Server) listModelSources(w http.ResponseWriter, r *http.Request) {
	service, ok := s.modelSources()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "model_sources_unavailable", "Model source storage is not configured")
		return
	}
	sources, err := service.List(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	if sources == nil {
		sources = []domain.ModelSource{}
	}
	writeJSON(w, http.StatusOK, struct {
		Sources []domain.ModelSource `json:"sources"`
	}{sources})
}

func (s *Server) createModelSource(w http.ResponseWriter, r *http.Request) {
	service, ok := s.modelSources()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "model_sources_unavailable", "Model source storage is not configured")
		return
	}
	var request application.ModelSourceCreateRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	source, err := service.Create(r.Context(), request)
	if err != nil {
		s.failModelSource(w, err)
		return
	}
	writeJSON(w, http.StatusOK, source)
}

func (s *Server) updateModelSource(w http.ResponseWriter, r *http.Request) {
	service, ok := s.modelSources()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "model_sources_unavailable", "Model source storage is not configured")
		return
	}
	var request application.ModelSourceUpdateRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	source, err := service.Update(r.Context(), r.PathValue("id"), request)
	if err != nil {
		s.failModelSource(w, err)
		return
	}
	writeJSON(w, http.StatusOK, source)
}

func (s *Server) deleteModelSource(w http.ResponseWriter, r *http.Request) {
	service, ok := s.modelSources()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "model_sources_unavailable", "Model source storage is not configured")
		return
	}
	if err := service.Delete(r.Context(), r.PathValue("id")); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
