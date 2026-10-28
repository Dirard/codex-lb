package httpapi

import (
	"context"
	"net/http"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

// dashboardStatus is the status bar's usage-sync signal. It reads only
// accounts and their current quota rows, without the traffic aggregation,
// credit and usage reads of the full dashboard overview.
type dashboardStatus struct {
	LastSyncAt *time.Time `json:"lastSyncAt"`
}

// registerDashboardStatusRoute is mounted inside the authenticated /api mux.
func (s *Server) registerDashboardStatusRoute(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/dashboard/status", func(w http.ResponseWriter, r *http.Request) {
		lastSyncAt, err := latestQuotaSync(r.Context(), s.store)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, dashboardStatus{LastSyncAt: lastSyncAt})
	})
}

// latestQuotaSync returns the same value as the dashboard overview's
// lastSyncAt: the newest quota observation among ChatGPT accounts that are
// not deleted, or nil when no quota was ever observed.
func latestQuotaSync(ctx context.Context, store application.Accounts) (*time.Time, error) {
	accounts, err := store.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	var latest *time.Time
	for _, account := range accounts {
		if account.Kind != domain.AccountChatGPT || account.Status == domain.AccountDeactivated && account.DeactivationReason == "deleted" {
			continue
		}
		quotas, err := store.ListAccountQuota(ctx, account.ID)
		if err != nil {
			return nil, err
		}
		for _, quota := range quotas {
			if latest == nil || quota.ObservedAt.After(*latest) {
				observed := quota.ObservedAt
				latest = &observed
			}
		}
	}
	return latest, nil
}
