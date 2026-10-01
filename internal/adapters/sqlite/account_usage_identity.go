package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"codex-lb/internal/domain"
)

// prepareUsageIdentityTx fences older fetches and handles the legacy two-sample
// confirmation for a workspace-less paid-to-free downgrade. It updates only
// identity metadata; status, operator policy and ciphertext are never written.
func prepareUsageIdentityTx(ctx context.Context, tx *sql.Tx, snapshot domain.AccountUsageSnapshot) (bool, error) {
	started := snapshot.FetchStartedAt
	if started.IsZero() {
		started = snapshot.ObservedAt
	}
	var current domain.Account
	var lastRefresh sql.NullInt64
	var egress bool
	err := tx.QueryRowContext(ctx, `SELECT id,kind,chatgpt_account_id,chatgpt_user_id,email,
 workspace_id,workspace_label,seat_type,plan_type,status,requires_egress_decision,last_refresh,generation
 FROM accounts WHERE id=?`, snapshot.AccountID).Scan(&current.ID, &current.Kind,
		&current.ChatGPTAccountID, &current.ChatGPTUserID, &current.Email,
		&current.WorkspaceID, &current.WorkspaceLabel, &current.SeatType, &current.PlanType,
		&current.Status, &egress, &lastRefresh, &current.Generation)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, err
	}
	current.LastRefresh = optionalTime(lastRefresh)
	if snapshot.ExpectedAccount != nil {
		expected := snapshot.ExpectedAccount
		if snapshot.ExpectedCredential == nil || snapshot.ExpectedCredential.AccountID != snapshot.AccountID {
			return false, ErrInvalid
		}
		var access, refresh, identity []byte
		if err := tx.QueryRowContext(ctx, `SELECT access_token_encrypted,refresh_token_encrypted,id_token_encrypted
 FROM account_credentials WHERE account_id=?`, snapshot.AccountID).Scan(&access, &refresh, &identity); err != nil {
			return false, err
		}
		credential := snapshot.ExpectedCredential
		if credential.Generation != current.Generation {
			return false, ErrConflict
		}
		if !bytes.Equal(access, credential.AccessTokenEncrypted) || !bytes.Equal(refresh, credential.RefreshTokenEncrypted) ||
			!bytes.Equal(identity, credential.IDTokenEncrypted) {
			return false, ErrConflict
		}
		if expected.ID != current.ID || expected.Generation != current.Generation || current.Kind != domain.AccountChatGPT ||
			expected.ChatGPTAccountID != current.ChatGPTAccountID || expected.ChatGPTUserID != current.ChatGPTUserID ||
			expected.Email != current.Email || expected.WorkspaceID != current.WorkspaceID ||
			expected.WorkspaceLabel != current.WorkspaceLabel || expected.SeatType != current.SeatType ||
			expected.PlanType != current.PlanType || expected.Status != current.Status ||
			expected.RequiresEgressDecision != egress || !sameOptionalTime(expected.LastRefresh, current.LastRefresh) {
			return false, ErrConflict
		}
	} else if snapshot.ExpectedCredential != nil || snapshot.ReportedPlanType != "" || snapshot.ReportedWorkspaceID != "" ||
		snapshot.ReportedWorkspaceLabel != "" || snapshot.ReportedSeatType != "" {
		return false, ErrInvalid
	}
	var previousStart int64
	var pendingFrom string
	var pendingCount int
	err = tx.QueryRowContext(ctx, `SELECT fetch_started_at,pending_free_from_plan,pending_free_count
 FROM account_usage_observation WHERE account_id=?`, snapshot.AccountID).Scan(&previousStart, &pendingFrom, &pendingCount)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if err == nil && previousStart >= millis(started) {
		return false, ErrConflict
	}
	plan := strings.ToLower(strings.TrimSpace(snapshot.ReportedPlanType))
	if plan != "" && plan != "free" && !knownPaidPlan(plan) {
		return false, ErrInvalid
	}
	workspace := strings.TrimSpace(snapshot.ReportedWorkspaceID)
	if current.WorkspaceID != "" && workspace != "" && workspace != current.WorkspaceID {
		return false, ErrInvalid
	}
	if current.WorkspaceID == "" && workspace != "" {
		var taken int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM accounts WHERE id<>? AND email=?
 AND chatgpt_account_id=? AND workspace_id=?`, current.ID, current.Email, current.ChatGPTAccountID, workspace).Scan(&taken); err != nil {
			return false, err
		}
		if taken != 0 {
			return false, ErrConflict
		}
	}
	pending := false
	if plan == "free" && current.PlanType != "free" {
		if current.WorkspaceID != "" && workspace == "" {
			return false, ErrInvalid
		}
		if current.WorkspaceID == "" && workspace == "" {
			if !knownPaidPlan(current.PlanType) {
				return false, ErrInvalid
			}
			if pendingFrom == current.PlanType {
				pendingCount++
			} else {
				pendingCount = 1
			}
			pendingFrom = current.PlanType
			pending = pendingCount < 2
		}
	}
	if knownPaidPlan(plan) || plan == "free" && !pending {
		pendingFrom, pendingCount = "", 0
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO account_usage_observation
 (account_id,fetch_started_at,pending_free_from_plan,pending_free_count) VALUES(?,?,?,?)
 ON CONFLICT(account_id) DO UPDATE SET fetch_started_at=excluded.fetch_started_at,
 pending_free_from_plan=excluded.pending_free_from_plan,pending_free_count=excluded.pending_free_count`,
		snapshot.AccountID, millis(started), pendingFrom, pendingCount); err != nil {
		return false, err
	}
	if pending {
		return true, nil
	}
	nextPlan := current.PlanType
	if plan != "" {
		nextPlan = plan
	}
	nextWorkspace := current.WorkspaceID
	if workspace != "" {
		nextWorkspace = workspace
	}
	nextLabel := current.WorkspaceLabel
	if label := strings.TrimSpace(snapshot.ReportedWorkspaceLabel); label != "" {
		nextLabel = label
	}
	nextSeat := current.SeatType
	if seat := strings.TrimSpace(snapshot.ReportedSeatType); seat != "" {
		nextSeat = seat
	}
	if nextPlan != current.PlanType || nextWorkspace != current.WorkspaceID ||
		nextLabel != current.WorkspaceLabel || nextSeat != current.SeatType {
		if _, err := tx.ExecContext(ctx, `UPDATE accounts SET plan_type=?,workspace_id=?,workspace_label=?,seat_type=?
 WHERE id=?`, nextPlan, nextWorkspace, nextLabel, nextSeat, current.ID); err != nil {
			return false, err
		}
	}
	return false, nil
}

func knownPaidPlan(plan string) bool {
	switch strings.ToLower(strings.TrimSpace(plan)) {
	case "plus", "pro", "prolite", "team", "business", "enterprise", "edu":
		return true
	default:
		return false
	}
}

func sameOptionalTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return millis(*a) == millis(*b)
}
