package httpapi

import (
	"errors"
	"net/http"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

// ConfigureBackups supplies the online backup use case before Handler is built.
func (s *Server) ConfigureBackups(service *application.BackupService) { s.backups = service }

// registerBackupRoutes exposes backup administration behind requireAdmin. The
// global cross-origin protection covers the unsafe methods; the download also
// refuses cross-site fetches because it is a GET.
func (s *Server) registerBackupRoutes(admin *http.ServeMux) {
	admin.HandleFunc("GET /api/backups", func(w http.ResponseWriter, r *http.Request) {
		status, err := s.backups.Status(r.Context())
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, status)
	})
	admin.HandleFunc("POST /api/backups", func(w http.ResponseWriter, r *http.Request) {
		if !emptyUpdateBody(w, r) {
			return
		}
		file, err := s.backups.Create(r.Context(), domain.BackupTriggerManual)
		if err != nil {
			s.writeBackupError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, file)
	})
	admin.HandleFunc("PUT /api/backups/schedule", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			IntervalHours *int `json:"intervalHours"`
			Keep          *int `json:"keep"`
		}
		if !decodeJSON(w, r, &request) {
			return
		}
		if request.IntervalHours == nil || request.Keep == nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid_backup_schedule", "intervalHours and keep are required")
			return
		}
		schedule, err := s.backups.UpdateSchedule(r.Context(), domain.BackupSchedule{IntervalHours: *request.IntervalHours, Keep: *request.Keep})
		if errors.Is(err, domain.ErrInvalid) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_backup_schedule", "intervalHours must be 0-168 and keep 1-30")
			return
		}
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, schedule)
	})
	admin.HandleFunc("GET /api/backups/{name}", func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Sec-Fetch-Site") {
		case "", "same-origin", "none":
		default:
			writeError(w, http.StatusForbidden, "cross_origin_request", "Cross-origin request rejected")
			return
		}
		file, info, err := s.backups.Open(r.PathValue("name"))
		if err != nil {
			s.fail(w, err)
			return
		}
		defer file.Close()
		w.Header().Set("Content-Type", "application/vnd.sqlite3")
		w.Header().Set("Content-Disposition", `attachment; filename="`+info.Name+`"`)
		w.Header().Set("Cache-Control", "no-store")
		http.ServeContent(w, r, info.Name, info.CreatedAt, file)
	})
}

func (s *Server) writeBackupError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrBackupBusy):
		writeError(w, http.StatusConflict, "backup_busy", "Another backup is running")
	case errors.Is(err, domain.ErrInsufficientStorage):
		writeError(w, http.StatusInsufficientStorage, "insufficient_storage", "Not enough free disk space for a backup")
	case errors.Is(err, domain.ErrConflict):
		writeError(w, http.StatusConflict, "backup_exists", "A backup was created in the same second")
	default:
		s.logger.Error("backup failed", "error_type", errorType(err))
		writeError(w, http.StatusInternalServerError, "backup_failed", "The backup could not be created or verified")
	}
}
