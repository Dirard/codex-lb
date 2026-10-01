package sqlite

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"time"

	"codex-lb/internal/domain"
)

const accountColumns = `id,kind,provider,base_url,chatgpt_account_id,chatgpt_user_id,codex_installation_id,
 email,alias,workspace_id,workspace_label,seat_type,plan_type,routing_policy,status,
 deactivation_reason,requires_egress_decision,security_work_authorized,limit_warmup_enabled,
 created_at,last_refresh,generation,route_revision`

type scanner interface{ Scan(...any) error }

type accountWriter interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func scanAccount(row scanner) (domain.Account, error) {
	var a domain.Account
	var created int64
	var last sql.NullInt64
	err := row.Scan(&a.ID, &a.Kind, &a.Provider, &a.BaseURL, &a.ChatGPTAccountID,
		&a.ChatGPTUserID, &a.CodexInstallationID, &a.Email, &a.Alias, &a.WorkspaceID,
		&a.WorkspaceLabel, &a.SeatType, &a.PlanType, &a.RoutingPolicy, &a.Status,
		&a.DeactivationReason, &a.RequiresEgressDecision, &a.SecurityWorkAuthorized,
		&a.LimitWarmupEnabled, &created, &last, &a.Generation, &a.RouteRevision)
	if err != nil {
		return a, err
	}
	a.CreatedAt = fromMillis(created)
	a.LastRefresh = optionalTime(last)
	a.DisplayName = a.Alias
	if a.DisplayName == "" {
		a.DisplayName = a.Email
	}
	if a.DisplayName == "" {
		a.DisplayName = a.ID
	}
	return a, nil
}

func (s *Store) SaveAccount(ctx context.Context, a domain.Account) error {
	return saveAccount(ctx, s.db, a)
}

func saveAccount(ctx context.Context, db accountWriter, a domain.Account) error {
	if a.ID == "" || a.Generation < 0 || a.RouteRevision < 0 || (a.Kind != domain.AccountChatGPT && a.Kind != domain.AccountExternal) {
		return fmt.Errorf("account id/kind: %w", ErrInvalid)
	}
	if a.Status == "" {
		a.Status = domain.AccountActive
	}
	if a.Kind == domain.AccountChatGPT && a.Provider == "" {
		a.Provider = "openai"
	}
	if a.Provider == "" {
		return fmt.Errorf("account provider: %w", ErrInvalid)
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now().UTC()
	}
	res, err := db.ExecContext(ctx, `INSERT INTO accounts (
 id,kind,provider,base_url,chatgpt_account_id,chatgpt_user_id,codex_installation_id,
 email,alias,workspace_id,workspace_label,seat_type,plan_type,routing_policy,status,
 deactivation_reason,requires_egress_decision,security_work_authorized,limit_warmup_enabled,created_at,last_refresh,generation,route_revision
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET kind=excluded.kind,provider=excluded.provider,base_url=excluded.base_url,
 chatgpt_account_id=excluded.chatgpt_account_id,chatgpt_user_id=excluded.chatgpt_user_id,
 codex_installation_id=excluded.codex_installation_id,email=excluded.email,alias=excluded.alias,
 workspace_id=excluded.workspace_id,workspace_label=excluded.workspace_label,seat_type=excluded.seat_type,
 plan_type=excluded.plan_type,routing_policy=excluded.routing_policy,status=excluded.status,
 deactivation_reason=excluded.deactivation_reason,requires_egress_decision=excluded.requires_egress_decision,
 security_work_authorized=excluded.security_work_authorized,limit_warmup_enabled=excluded.limit_warmup_enabled,
 last_refresh=excluded.last_refresh
 WHERE accounts.kind=excluded.kind AND accounts.provider=excluded.provider
 AND accounts.base_url=excluded.base_url AND accounts.generation=excluded.generation
 AND accounts.route_revision=excluded.route_revision
 AND NOT(accounts.status='deactivated' AND accounts.deactivation_reason='deleted')`, a.ID, a.Kind, a.Provider, a.BaseURL, a.ChatGPTAccountID,
		a.ChatGPTUserID, a.CodexInstallationID, a.Email, a.Alias, a.WorkspaceID, a.WorkspaceLabel,
		a.SeatType, a.PlanType, a.RoutingPolicy, a.Status, a.DeactivationReason,
		boolInt(a.RequiresEgressDecision), boolInt(a.SecurityWorkAuthorized), boolInt(a.LimitWarmupEnabled),
		millis(a.CreatedAt), optionalMillis(a.LastRefresh), a.Generation, a.RouteRevision)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("account provider identity changed: %w", ErrConflict)
	}
	return nil
}

func (s *Store) GetAccount(ctx context.Context, id string) (domain.Account, error) {
	a, err := scanAccount(s.readDB.QueryRowContext(ctx, "SELECT "+accountColumns+" FROM accounts WHERE id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}

func (s *Store) ListAccounts(ctx context.Context) ([]domain.Account, error) {
	rows, err := s.readDB.QueryContext(ctx, "SELECT "+accountColumns+" FROM accounts WHERE NOT(status='deactivated' AND deactivation_reason='deleted') ORDER BY created_at,id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]domain.Account, 0)
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, a)
	}
	return result, rows.Err()
}

func fernetCiphertext(v []byte) bool {
	if len(v) == 0 {
		return true
	}
	decoded, err := base64.URLEncoding.DecodeString(string(v))
	return err == nil && len(decoded) >= 73 && decoded[0] == 0x80
}

func (s *Store) SaveAccountCredential(ctx context.Context, c domain.AccountCredential) error {
	return transact(ctx, s.db, func(tx *sql.Tx) error { return saveAccountCredential(ctx, tx, c) })
}

func saveAccountCredential(ctx context.Context, db accountWriter, c domain.AccountCredential) error {
	if c.AccountID == "" || c.Generation < 0 || c.RouteRevision < 0 || !fernetCiphertext(c.AccessTokenEncrypted) ||
		!fernetCiphertext(c.RefreshTokenEncrypted) || !fernetCiphertext(c.IDTokenEncrypted) ||
		!fernetCiphertext(c.ExternalKeyEncrypted) {
		return fmt.Errorf("account ciphertext: %w", ErrInvalid)
	}
	var kind domain.AccountKind
	if err := db.QueryRowContext(ctx, `SELECT kind FROM accounts WHERE id=? AND generation=? AND route_revision=?
 AND NOT(status='deactivated' AND deactivation_reason='deleted')`, c.AccountID, c.Generation, c.RouteRevision).Scan(&kind); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if kind == domain.AccountChatGPT && (len(c.AccessTokenEncrypted) == 0 || len(c.RefreshTokenEncrypted) == 0 || len(c.IDTokenEncrypted) == 0) {
		return fmt.Errorf("ChatGPT tokens: %w", ErrInvalid)
	}
	_, err := db.ExecContext(ctx, `INSERT INTO account_credentials
 (account_id,access_token_encrypted,refresh_token_encrypted,id_token_encrypted,external_key_encrypted)
 VALUES (?,?,?,?,?) ON CONFLICT(account_id) DO UPDATE SET
 access_token_encrypted=excluded.access_token_encrypted,
 refresh_token_encrypted=excluded.refresh_token_encrypted,
 id_token_encrypted=excluded.id_token_encrypted,
 external_key_encrypted=excluded.external_key_encrypted`,
		c.AccountID, c.AccessTokenEncrypted, c.RefreshTokenEncrypted, c.IDTokenEncrypted, c.ExternalKeyEncrypted)
	return err
}

func (s *Store) GetAccountCredential(ctx context.Context, id string) (domain.AccountCredential, error) {
	c := domain.AccountCredential{AccountID: id}
	err := s.readDB.QueryRowContext(ctx, `SELECT c.access_token_encrypted,c.refresh_token_encrypted,
 c.id_token_encrypted,c.external_key_encrypted,a.generation,a.route_revision FROM account_credentials c
 JOIN accounts a ON a.id=c.account_id
 WHERE c.account_id=? AND NOT(a.status='deactivated' AND a.deactivation_reason='deleted')`, id).Scan(
		&c.AccessTokenEncrypted, &c.RefreshTokenEncrypted, &c.IDTokenEncrypted, &c.ExternalKeyEncrypted, &c.Generation, &c.RouteRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

func (s *Store) SaveAccountQuota(ctx context.Context, q domain.AccountQuota) error {
	return transact(ctx, s.db, func(tx *sql.Tx) error { return saveAccountQuotaTx(ctx, tx, q) })
}

func saveAccountQuotaTx(ctx context.Context, tx *sql.Tx, q domain.AccountQuota) error {
	if q.AccountID == "" || q.Window == "" || math.IsNaN(q.UsedPercent) || math.IsInf(q.UsedPercent, 0) || q.UsedPercent < 0 || q.ObservedAt.IsZero() {
		return fmt.Errorf("account quota: %w", ErrInvalid)
	}
	var minutes any
	if q.WindowMinutes != nil {
		minutes = *q.WindowMinutes
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO account_quotas
 (account_id,window,used_percent,reset_at,window_minutes,observed_at) VALUES (?,?,?,?,?,?)
 ON CONFLICT(account_id,window) DO UPDATE SET used_percent=excluded.used_percent,
 reset_at=excluded.reset_at,window_minutes=excluded.window_minutes,observed_at=excluded.observed_at
 WHERE excluded.observed_at>=account_quotas.observed_at`, q.AccountID, q.Window,
		q.UsedPercent, optionalMillis(q.ResetAt), minutes, millis(q.ObservedAt))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO account_quota_history
 (account_id,window,observed_at,used_percent,reset_at,window_minutes) VALUES(?,?,?,?,?,?)`,
		q.AccountID, q.Window, millis(q.ObservedAt), q.UsedPercent, optionalMillis(q.ResetAt), minutes)
	return err
}

func (s *Store) ListAccountQuota(ctx context.Context, id string) ([]domain.AccountQuota, error) {
	rows, err := s.readDB.QueryContext(ctx, `SELECT window,used_percent,reset_at,window_minutes,observed_at
 FROM account_quotas WHERE account_id=? ORDER BY window`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]domain.AccountQuota, 0)
	for rows.Next() {
		q := domain.AccountQuota{AccountID: id}
		var reset, minutes sql.NullInt64
		var observed int64
		if err := rows.Scan(&q.Window, &q.UsedPercent, &reset, &minutes, &observed); err != nil {
			return nil, err
		}
		q.ResetAt = optionalTime(reset)
		if minutes.Valid {
			m := int(minutes.Int64)
			q.WindowMinutes = &m
		}
		q.ObservedAt = fromMillis(observed)
		result = append(result, q)
	}
	return result, rows.Err()
}
