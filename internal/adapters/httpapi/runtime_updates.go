package httpapi

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"codex-lb/internal/domain"
)

const runtimeReleasesURL = "https://github.com/Dirard/codex-lb/releases"

func (s *Server) runtimeUpdateStatus(r *http.Request) (domain.RuntimeUpdateStatus, error) {
	if s.runtimeUpdates == nil {
		return domain.RuntimeUpdateStatus{
			CurrentVersion:    s.config.Version,
			ReleaseURL:        runtimeReleasesURL,
			UnavailableReason: "This installation is not managed by the runtime updater",
			Phase:             "idle",
		}, nil
	}
	return s.runtimeUpdates.Status(r.Context())
}

func (s *Server) registerRuntimeUpdateRoutes(admin *http.ServeMux) {
	admin.HandleFunc("GET /api/runtime/version", func(w http.ResponseWriter, r *http.Request) {
		status, err := s.runtimeUpdateStatus(r)
		if err != nil {
			s.fail(w, err)
			return
		}
		// Retain the original endpoint's field names for existing dashboard clients.
		var latestVersion, source *string
		if status.LatestVersion != "" {
			latestVersion = &status.LatestVersion
		}
		if status.Source != "" {
			source = &status.Source
		}
		checkedAt := time.Time{}
		if status.CheckedAt != nil {
			checkedAt = *status.CheckedAt
		}
		writeJSON(w, http.StatusOK, struct {
			CurrentVersion  string    `json:"currentVersion"`
			LatestVersion   *string   `json:"latestVersion"`
			UpdateAvailable bool      `json:"updateAvailable"`
			CheckedAt       time.Time `json:"checkedAt"`
			Source          *string   `json:"source"`
			ReleaseURL      string    `json:"releaseUrl"`
		}{status.CurrentVersion, latestVersion, status.UpdateAvailable, checkedAt, source, status.ReleaseURL})
	})
	admin.HandleFunc("GET /api/runtime/updates", func(w http.ResponseWriter, r *http.Request) {
		status, err := s.runtimeUpdateStatus(r)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, status)
	})
	admin.HandleFunc("POST /api/runtime/updates/check", func(w http.ResponseWriter, r *http.Request) {
		if !emptyUpdateBody(w, r) {
			return
		}
		if s.runtimeUpdates == nil {
			writeRuntimeUpdateError(w, domain.ErrUpdateUnavailable)
			return
		}
		status, err := s.runtimeUpdates.Check(r.Context())
		s.writeRuntimeUpdateAction(w, status, err)
	})
	for _, action := range []struct {
		path string
		run  func(*http.Request, string) (domain.RuntimeUpdateStatus, error)
	}{
		{"POST /api/runtime/updates/apply", func(r *http.Request, version string) (domain.RuntimeUpdateStatus, error) {
			return s.runtimeUpdates.Apply(r.Context(), version)
		}},
		{"POST /api/runtime/updates/rollback", func(r *http.Request, version string) (domain.RuntimeUpdateStatus, error) {
			return s.runtimeUpdates.Rollback(r.Context(), version)
		}},
	} {
		admin.HandleFunc(action.path, func(w http.ResponseWriter, r *http.Request) {
			var request struct {
				Version string `json:"version"`
			}
			if !decodeJSON(w, r, &request) {
				return
			}
			if request.Version == "" || strings.TrimSpace(request.Version) != request.Version {
				writeError(w, http.StatusBadRequest, "invalid_update_target", "A version is required")
				return
			}
			if s.runtimeUpdates == nil {
				writeRuntimeUpdateError(w, domain.ErrUpdateUnavailable)
				return
			}
			status, err := action.run(r, request.Version)
			s.writeRuntimeUpdateAction(w, status, err)
		})
	}
}

func emptyUpdateBody(w http.ResponseWriter, r *http.Request) bool {
	data, err := io.ReadAll(io.LimitReader(r.Body, 1))
	if err != nil || len(data) != 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "This action takes no request body")
		return false
	}
	return true
}

func (s *Server) writeRuntimeUpdateAction(w http.ResponseWriter, status domain.RuntimeUpdateStatus, err error) {
	if err != nil {
		if !writeRuntimeUpdateError(w, err) {
			s.fail(w, err)
		}
		return
	}
	writeJSON(w, http.StatusAccepted, status)
}

func writeRuntimeUpdateError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, domain.ErrUpdateBusy):
		writeError(w, http.StatusConflict, "update_busy", "Another runtime update operation is in progress")
	case errors.Is(err, domain.ErrUpdateUnavailable):
		writeError(w, http.StatusServiceUnavailable, "update_unavailable", "Runtime update is unavailable")
	case errors.Is(err, domain.ErrUpdateTarget):
		writeError(w, http.StatusConflict, "update_target_unavailable", "The requested runtime version is unavailable")
	default:
		return false
	}
	return true
}
