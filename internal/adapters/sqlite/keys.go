package sqlite

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"codex-lb/internal/domain"
)

const keyColumns = `id,name,key_hash,key_prefix,group_id,allowed_models,apply_to_codex_model,
 enforced_model,allowed_reasoning_efforts,enforced_reasoning_effort,enforced_service_tier,
 traffic_class,transport_policy_override,usage_sections,account_assignment_scope_enabled,
 source_assignment_scope_enabled,expires_at,is_active,created_at,last_used_at`

func encodeList(values []string) (any, error) {
	if values == nil {
		return nil, nil
	}
	seen := make(map[string]bool, len(values))
	for _, v := range values {
		if v == "" || seen[v] {
			return nil, fmt.Errorf("duplicate or empty list item: %w", ErrInvalid)
		}
		seen[v] = true
	}
	b, err := json.Marshal(values)
	return string(b), err
}

func decodeList(raw sql.NullString) ([]string, error) {
	if !raw.Valid {
		return nil, nil
	}
	var values []string
	if err := json.Unmarshal([]byte(raw.String), &values); err != nil {
		return nil, err
	}
	return values, nil
}

func nullableString(v *string) any {
	if v == nil {
		return nil
	}
	return *v
}

func optionalString(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	return &v.String
}

func scanKey(row scanner) (domain.APIKey, error) {
	var k domain.APIKey
	var group, allowed, enforcedModel, efforts, enforcedEffort, tier, transport sql.NullString
	var expires, lastUsed sql.NullInt64
	var created int64
	err := row.Scan(&k.ID, &k.Name, &k.KeyHash, &k.KeyPrefix, &group, &allowed,
		&k.ApplyToCodexModel, &enforcedModel, &efforts, &enforcedEffort, &tier,
		&k.TrafficClass, &transport, &k.UsageSections, &k.AccountAssignmentScopeEnabled,
		&k.SourceAssignmentScopeEnabled, &expires, &k.IsActive, &created, &lastUsed)
	if err != nil {
		return k, err
	}
	k.GroupID = optionalString(group)
	k.AllowedModels, err = decodeList(allowed)
	if err != nil {
		return k, err
	}
	k.AllowedReasoningEfforts, err = decodeList(efforts)
	if err != nil {
		return k, err
	}
	k.EnforcedModel = optionalString(enforcedModel)
	k.EnforcedReasoningEffort = optionalString(enforcedEffort)
	k.EnforcedServiceTier = optionalString(tier)
	k.TransportPolicyOverride = optionalString(transport)
	k.ExpiresAt = optionalTime(expires)
	k.CreatedAt = fromMillis(created)
	k.LastUsedAt = optionalTime(lastUsed)
	return k, nil
}

func (s *Store) SaveAPIKey(ctx context.Context, k domain.APIKey, now time.Time) error {
	if domain.IsInternalKey(k.ID) {
		return ErrInvalid
	}
	k.Name = strings.TrimSpace(k.Name)
	hash, hashErr := hex.DecodeString(k.KeyHash)
	if k.ID == "" || k.Name == "" || len(k.Name) > 128 || len(hash) != 32 || hashErr != nil ||
		k.KeyPrefix == "" || now.IsZero() || (k.GroupID != nil && *k.GroupID == "") ||
		(k.AllowedReasoningEfforts != nil && k.EnforcedReasoningEffort != nil) {
		return fmt.Errorf("API key: %w", ErrInvalid)
	}
	if k.TrafficClass == "" {
		k.TrafficClass = "foreground"
	}
	allowed, err := encodeList(k.AllowedModels)
	if err != nil {
		return err
	}
	efforts, err := encodeList(k.AllowedReasoningEfforts)
	if err != nil {
		return err
	}
	if err := validateRules(k.Limits); err != nil {
		return err
	}
	if k.CreatedAt.IsZero() {
		k.CreatedAt = now
	}
	return transact(ctx, s.db, func(tx *sql.Tx) error {
		var priorGroup sql.NullString
		err := tx.QueryRowContext(ctx, "SELECT group_id FROM api_keys WHERE id=? AND deleted_at IS NULL", k.ID).Scan(&priorGroup)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if k.GroupID != nil {
			var exists int
			if err := tx.QueryRowContext(ctx, "SELECT 1 FROM account_groups WHERE id=?", *k.GroupID).Scan(&exists); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return fmt.Errorf("unknown group: %w", ErrInvalid)
				}
				return err
			}
		}
		if err := validateAssignedAccounts(ctx, tx, k.AssignedAccountIDs, false); err != nil {
			return err
		}
		if err := validateAssignedAccounts(ctx, tx, k.AssignedSourceIDs, true); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO api_keys (
 id,name,key_hash,key_prefix,group_id,allowed_models,apply_to_codex_model,enforced_model,
 allowed_reasoning_efforts,enforced_reasoning_effort,enforced_service_tier,traffic_class,
 transport_policy_override,usage_sections,account_assignment_scope_enabled,
 source_assignment_scope_enabled,expires_at,is_active,created_at,last_used_at
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET
 name=excluded.name,key_hash=excluded.key_hash,key_prefix=excluded.key_prefix,group_id=excluded.group_id,
 allowed_models=excluded.allowed_models,apply_to_codex_model=excluded.apply_to_codex_model,
 enforced_model=excluded.enforced_model,allowed_reasoning_efforts=excluded.allowed_reasoning_efforts,
 enforced_reasoning_effort=excluded.enforced_reasoning_effort,
 enforced_service_tier=excluded.enforced_service_tier,traffic_class=excluded.traffic_class,
 transport_policy_override=excluded.transport_policy_override,usage_sections=excluded.usage_sections,
 account_assignment_scope_enabled=excluded.account_assignment_scope_enabled,
 source_assignment_scope_enabled=excluded.source_assignment_scope_enabled,
 expires_at=excluded.expires_at,is_active=excluded.is_active,last_used_at=excluded.last_used_at
 WHERE api_keys.deleted_at IS NULL`, k.ID, k.Name, k.KeyHash, k.KeyPrefix, nullableString(k.GroupID),
			allowed, boolInt(k.ApplyToCodexModel), nullableString(k.EnforcedModel), efforts,
			nullableString(k.EnforcedReasoningEffort), nullableString(k.EnforcedServiceTier), k.TrafficClass,
			nullableString(k.TransportPolicyOverride), k.UsageSections,
			boolInt(k.AccountAssignmentScopeEnabled), boolInt(k.SourceAssignmentScopeEnabled),
			optionalMillis(k.ExpiresAt), boolInt(k.IsActive), millis(k.CreatedAt), optionalMillis(k.LastUsedAt))
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrConflict
		}
		if err := replaceAssignments(ctx, tx, "api_key_accounts", k.ID, k.AssignedAccountIDs); err != nil {
			return err
		}
		if err := replaceAssignments(ctx, tx, "api_key_sources", k.ID, k.AssignedSourceIDs); err != nil {
			return err
		}
		if k.GroupID != nil {
			rules, err := groupRulesTx(ctx, tx, *k.GroupID)
			if err != nil {
				return err
			}
			return syncKeyLimitsTx(ctx, tx, k.ID, rules, now)
		}
		if priorGroup.Valid { // Leaving a group keeps its last effective per-key limits.
			return nil
		}
		if k.Limits != nil {
			return syncKeyLimitsTx(ctx, tx, k.ID, k.Limits, now)
		}
		return nil
	})
}

func validateAssignedAccounts(ctx context.Context, tx *sql.Tx, ids []string, external bool) error {
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id == "" || seen[id] {
			return fmt.Errorf("duplicate or empty account assignment: %w", ErrInvalid)
		}
		seen[id] = true
		var kind domain.AccountKind
		if err := tx.QueryRowContext(ctx, "SELECT kind FROM accounts WHERE id=? AND NOT(status='deactivated' AND deactivation_reason='deleted')", id).Scan(&kind); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("unknown assigned account %q: %w", id, ErrInvalid)
			}
			return err
		}
		if external && kind != domain.AccountExternal {
			return fmt.Errorf("assigned source %q is not external: %w", id, ErrInvalid)
		}
	}
	return nil
}

func replaceAssignments(ctx context.Context, tx *sql.Tx, table, keyID string, ids []string) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE api_key_id=?", keyID); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, "INSERT INTO "+table+"(api_key_id,account_id) VALUES(?,?)", keyID, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) GetAPIKey(ctx context.Context, id string) (domain.APIKey, error) {
	k, err := scanKey(s.readDB.QueryRowContext(ctx, "SELECT "+keyColumns+" FROM api_keys WHERE id=? AND deleted_at IS NULL", id))
	if errors.Is(err, sql.ErrNoRows) {
		return k, ErrNotFound
	}
	if err != nil {
		return k, err
	}
	k.AssignedAccountIDs, err = s.keyAssignments(ctx, "api_key_accounts", id)
	if err != nil {
		return k, err
	}
	k.AssignedSourceIDs, err = s.keyAssignments(ctx, "api_key_sources", id)
	if err != nil {
		return k, err
	}
	rows, err := s.readDB.QueryContext(ctx, `SELECT id,limit_type,limit_window,model_filter,max_value,current_value,reset_at
 FROM api_key_limits WHERE api_key_id=? AND is_active=1 ORDER BY limit_type,limit_window,model_filter`, id)
	if err != nil {
		return k, err
	}
	defer rows.Close()
	k.Limits = make([]domain.LimitRule, 0)
	for rows.Next() {
		var rule domain.LimitRule
		var model string
		var reset int64
		if err := rows.Scan(&rule.ID, &rule.Type, &rule.Window, &model, &rule.MaxValue, &rule.CurrentValue, &reset); err != nil {
			return k, err
		}
		rule.ModelFilter = optionalModel(model)
		rule.ResetAt = fromMillis(reset)
		k.Limits = append(k.Limits, rule)
	}
	return k, rows.Err()
}

func (s *Store) keyAssignments(ctx context.Context, table, id string) ([]string, error) {
	rows, err := s.readDB.QueryContext(ctx, "SELECT account_id FROM "+table+" WHERE api_key_id=? ORDER BY account_id", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var accountID string
		if err := rows.Scan(&accountID); err != nil {
			return nil, err
		}
		ids = append(ids, accountID)
	}
	return ids, rows.Err()
}

func (s *Store) FindAPIKeyByHash(ctx context.Context, hash string) (domain.APIKey, error) {
	var id string
	err := s.readDB.QueryRowContext(ctx, "SELECT id FROM api_keys WHERE key_hash=? AND deleted_at IS NULL AND id NOT IN (?,?)", hash, domain.LocalProxyKeyID, domain.WarmupKeyID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.APIKey{}, ErrNotFound
	}
	if err != nil {
		return domain.APIKey{}, err
	}
	return s.GetAPIKey(ctx, id)
}

func (s *Store) ListAPIKeys(ctx context.Context) ([]domain.APIKey, error) {
	rows, err := s.readDB.QueryContext(ctx, "SELECT id FROM api_keys WHERE deleted_at IS NULL AND id NOT IN (?,?) ORDER BY created_at,id", domain.LocalProxyKeyID, domain.WarmupKeyID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	keys := make([]domain.APIKey, 0, len(ids))
	for _, id := range ids {
		k, err := s.GetAPIKey(ctx, id)
		if err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, nil
}

func (s *Store) DeleteAPIKey(ctx context.Context, id string) error {
	if domain.IsInternalKey(id) {
		return ErrInvalid
	}
	res, err := s.db.ExecContext(ctx, `UPDATE api_keys SET is_active=0,deleted_at=?
 WHERE id=? AND deleted_at IS NULL`, time.Now().UTC().UnixMilli(), id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ResetAPIKeyUsage starts new key limit windows; old reservations remain settleable
// against their original reset timestamp and cannot charge the new windows.
func (s *Store) ResetAPIKeyUsage(ctx context.Context, id string, now time.Time) error {
	if domain.IsInternalKey(id) {
		return ErrInvalid
	}
	if id == "" || now.IsZero() {
		return ErrInvalid
	}
	return transact(ctx, s.db, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM api_keys WHERE id=? AND deleted_at IS NULL`, id).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT id,limit_window FROM api_key_limits
 WHERE api_key_id=? AND is_active=1`, id)
		if err != nil {
			return err
		}
		type limit struct {
			id     int64
			window domain.LimitWindow
		}
		var limits []limit
		for rows.Next() {
			var l limit
			if err := rows.Scan(&l.id, &l.window); err != nil {
				rows.Close()
				return err
			}
			limits = append(limits, l)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, l := range limits {
			duration, err := l.window.Duration()
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE api_key_limits SET current_value=0,reset_at=?,
	 backfill_from=NULL,backfill_until=NULL WHERE id=?`,
				millis(now.Add(duration)), l.id); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) EligibleAccounts(ctx context.Context, keyID string) ([]domain.Account, error) {
	return s.eligibleAccounts(ctx, keyID, false)
}

// QuotaCandidateAccounts retains quota-exceeded ChatGPT accounts for the
// proxy's credit-aware check. Other consumers keep the active-only contract.
func (s *Store) QuotaCandidateAccounts(ctx context.Context, keyID string) ([]domain.Account, error) {
	return s.eligibleAccounts(ctx, keyID, true)
}

func (s *Store) eligibleAccounts(ctx context.Context, keyID string, includeQuotaExceeded bool) ([]domain.Account, error) {
	var group sql.NullString
	var accountScoped, sourceScoped, active bool
	var expires sql.NullInt64
	err := s.readDB.QueryRowContext(ctx, `SELECT group_id,account_assignment_scope_enabled,
 source_assignment_scope_enabled,is_active,expires_at FROM api_keys WHERE id=? AND deleted_at IS NULL`, keyID).
		Scan(&group, &accountScoped, &sourceScoped, &active, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if !active || (expires.Valid && expires.Int64 < time.Now().UTC().UnixMilli()) {
		return nil, ErrNoAccounts
	}
	statusGate := "a.status='active'"
	if includeQuotaExceeded {
		statusGate = "(a.status='active' OR (a.status='quota_exceeded' AND a.kind='chatgpt'))"
	}
	query := "SELECT " + accountColumns + " FROM accounts a WHERE " + statusGate + " AND a.requires_egress_decision=0 AND EXISTS(SELECT 1 FROM account_credentials c WHERE c.account_id=a.id)"
	args := make([]any, 0, 3)
	if group.Valid {
		query += " AND EXISTS(SELECT 1 FROM account_group_accounts g WHERE g.group_id=? AND g.account_id=a.id)"
		args = append(args, group.String)
	}
	if accountScoped && !group.Valid {
		query += " AND EXISTS(SELECT 1 FROM api_key_accounts ka WHERE ka.api_key_id=? AND ka.account_id=a.id)"
		args = append(args, keyID)
	}
	if sourceScoped {
		query += " AND (a.kind='chatgpt' OR EXISTS(SELECT 1 FROM api_key_sources ks WHERE ks.api_key_id=? AND ks.account_id=a.id))"
		args = append(args, keyID)
	}
	query += " ORDER BY a.id"
	rows, err := s.readDB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	accounts := make([]domain.Account, 0)
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(accounts) == 0 {
		return nil, ErrNoAccounts
	}
	return accounts, nil
}

// ScopedAccountForOwner checks current authorization for a previously pinned
// account. Telemetry exhaustion is not an authorization failure for that owner.
func (s *Store) ScopedAccountForOwner(ctx context.Context, keyID, accountID string, now time.Time) (domain.Account, error) {
	var account domain.Account
	if keyID == "" || accountID == "" || now.IsZero() {
		return account, ErrNoAccounts
	}
	tx, err := s.readDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return account, err
	}
	defer tx.Rollback()
	var group sql.NullString
	var expires sql.NullInt64
	var active, accountScoped, sourceScoped bool
	err = tx.QueryRowContext(ctx, `SELECT group_id,expires_at,is_active,
 account_assignment_scope_enabled,source_assignment_scope_enabled
 FROM api_keys WHERE id=? AND deleted_at IS NULL`, keyID).Scan(&group, &expires, &active,
		&accountScoped, &sourceScoped)
	if errors.Is(err, sql.ErrNoRows) {
		return account, ErrNoAccounts
	}
	if err != nil {
		return account, err
	}
	if !active || expires.Valid && expires.Int64 <= millis(now) {
		return account, ErrNoAccounts
	}
	account, err = scanAccount(tx.QueryRowContext(ctx, "SELECT "+accountColumns+" FROM accounts WHERE id=?", accountID))
	if errors.Is(err, sql.ErrNoRows) {
		return account, ErrNoAccounts
	}
	if err != nil {
		return account, err
	}
	if account.RequiresEgressDecision || (account.Status != domain.AccountActive &&
		account.Status != domain.AccountRateLimited && account.Status != domain.AccountQuotaExceeded) {
		return domain.Account{}, ErrNoAccounts
	}
	var exists int
	if err := tx.QueryRowContext(ctx, "SELECT 1 FROM account_credentials WHERE account_id=?", accountID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Account{}, ErrNoAccounts
		}
		return domain.Account{}, err
	}
	if group.Valid {
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM account_group_accounts
 WHERE group_id=? AND account_id=?`, group.String, accountID).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return domain.Account{}, ErrNoAccounts
			}
			return domain.Account{}, err
		}
	} else if accountScoped {
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM api_key_accounts
 WHERE api_key_id=? AND account_id=?`, keyID, accountID).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return domain.Account{}, ErrNoAccounts
			}
			return domain.Account{}, err
		}
	}
	if sourceScoped && account.Kind == domain.AccountExternal {
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM api_key_sources
 WHERE api_key_id=? AND account_id=?`, keyID, accountID).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return domain.Account{}, ErrNoAccounts
			}
			return domain.Account{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.Account{}, err
	}
	return account, nil
}
