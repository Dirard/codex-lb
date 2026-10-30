package httpapi

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

// failingAccountsRepository makes the account list fail with a chosen error,
// as SQLite does when the request context is cancelled mid-read.
type failingAccountsRepository struct {
	Repository
	err error
}

func (r failingAccountsRepository) ListAccountViews(context.Context, time.Time) (domain.AccountViews, error) {
	return domain.AccountViews{}, r.err
}

// Regression (2026-10-30): a browser that dropped dashboard requests during a
// network stall produced 500 internal_error and "admin operation failed" lines.
func TestCancelledDashboardRequestIsNotAServerError(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		status int
		code   string
		logged bool
	}{
		{"client cancelled", context.Canceled, statusClientClosedRequest, "request_cancelled", false},
		{"wrapped cancellation", errors.Join(errors.New("read accounts"), context.Canceled), statusClientClosedRequest, "request_cancelled", false},
		{"unexpected failure", errors.New("disk failure"), http.StatusInternalServerError, "internal_error", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, _, cookie := newAccountsListServer(t, func(store Repository) Repository {
				return failingAccountsRepository{Repository: store, err: test.err}
			})
			var logs bytes.Buffer
			server.logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			req := httptest.NewRequest(http.MethodGet, "http://localhost/api/accounts", nil)
			req.AddCookie(cookie)
			ctx, cancel := context.WithTimeout(req.Context(), 5*time.Second)
			defer cancel()
			rec := httptest.NewRecorder()
			server.Handler(nil, nil).ServeHTTP(rec, req.WithContext(ctx))
			if rec.Code != test.status || !strings.Contains(rec.Body.String(), `"`+test.code+`"`) {
				t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
			}
			if logged := strings.Contains(logs.String(), "admin operation failed"); logged != test.logged {
				t.Fatalf("admin operation failed logged=%v, want %v: %s", logged, test.logged, logs.String())
			}
		})
	}
}
