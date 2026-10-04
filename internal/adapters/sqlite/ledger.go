package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"codex-lb/internal/domain"
)

func (s *Store) ReserveUsage(ctx context.Context, req domain.ReservationRequest) (domain.Reservation, error) {
	r := domain.Reservation{ID: req.ID, APIKeyID: req.APIKeyID, AccountID: req.AccountID, Model: req.Model,
		AccountGeneration: req.AccountGeneration, RouteRevision: req.RouteRevision, Status: "reserved", CreatedAt: req.Now.UTC(), UpdatedAt: req.Now.UTC()}
	if req.ID == "" || req.APIKeyID == "" || req.APIKeyID == domain.WarmupKeyID || req.Model == "" || req.Now.IsZero() || req.AccountGeneration < 0 || req.RouteRevision < 0 || (req.Continuation && req.AccountID == "") {
		return r, fmt.Errorf("reservation identity: %w", ErrInvalid)
	}
	if err := req.Budget.Validate(); err != nil {
		return r, err
	}
	err := s.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var group sql.NullString
		var active, accountScoped, sourceScoped bool
		var expires sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT group_id,is_active,expires_at,
 account_assignment_scope_enabled,source_assignment_scope_enabled FROM api_keys
 WHERE id=? AND deleted_at IS NULL`, req.APIKeyID).
			Scan(&group, &active, &expires, &accountScoped, &sourceScoped); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if !active || (expires.Valid && expires.Int64 < millis(req.Now)) {
			return ErrNoAccounts
		}
		if req.AccountID != "" {
			if err := checkReservedAccountTx(ctx, tx, req, group, accountScoped, sourceScoped); err != nil {
				return err
			}
		} else {
			if err := checkAnyReservedAccountTx(ctx, tx, req.APIKeyID, group, accountScoped, sourceScoped); err != nil {
				return err
			}
		}
		rows, err := tx.QueryContext(ctx, `SELECT id,limit_type,limit_window,model_filter,max_value,current_value,reset_at
 FROM api_key_limits WHERE api_key_id=? AND is_active=1 ORDER BY id`, req.APIKeyID)
		if err != nil {
			return err
		}
		type limitRow struct {
			id, max, current, reset int64
			typ                     domain.LimitType
			window                  domain.LimitWindow
			model                   string
		}
		limits := make([]limitRow, 0)
		for rows.Next() {
			var l limitRow
			if err := rows.Scan(&l.id, &l.typ, &l.window, &l.model, &l.max, &l.current, &l.reset); err != nil {
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
		if _, err := tx.ExecContext(ctx, `INSERT INTO usage_reservations
	 (id,api_key_id,account_id,account_generation,route_revision,model,status,created_at,updated_at) VALUES(?,?,?,?,?,?,'reserved',?,?)`,
			req.ID, req.APIKeyID, req.AccountID, req.AccountGeneration, req.RouteRevision, req.Model, millis(req.Now), millis(req.Now)); err != nil {
			return err
		}
		for _, l := range limits {
			if l.model != "" && l.model != req.Model {
				continue
			}
			if l.reset <= millis(req.Now) {
				duration, err := l.window.Duration()
				if err != nil {
					return err
				}
				l.reset = advanceReset(l.reset, req.Now, duration)
				l.current = 0
				if _, err := tx.ExecContext(ctx, `UPDATE api_key_limits SET current_value=0,reset_at=?,
 backfill_from=NULL,backfill_until=NULL WHERE id=?`, l.reset, l.id); err != nil {
					return err
				}
			}
			if l.current >= l.max {
				return fmt.Errorf("%s %s: %w", l.typ, l.window, ErrLimitReached)
			}
			delta := req.Budget.ForLimit(l.typ)
			if l.typ != domain.LimitCredits && delta <= 0 {
				return fmt.Errorf("missing %s reservation budget: %w", l.typ, ErrInvalid)
			}
			remaining := l.max - l.current
			if delta > remaining {
				delta = remaining
			}
			if delta > 0 {
				if _, err := tx.ExecContext(ctx, `UPDATE api_key_limits SET current_value=current_value+?
 WHERE id=? AND reset_at=?`, delta, l.id, l.reset); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO usage_reservation_items
 (reservation_id,limit_id,limit_type,reserved_delta,expected_reset_at) VALUES(?,?,?,?,?)`,
				req.ID, l.id, l.typ, delta, l.reset); err != nil {
				return err
			}
		}
		return nil
	})
	return r, err
}

func checkReservedAccountTx(ctx context.Context, tx *sql.Tx, req domain.ReservationRequest, group sql.NullString, accountScoped, sourceScoped bool) error {
	var kind domain.AccountKind
	var status domain.AccountStatus
	var plan string
	var blocked bool
	var generation, revision int64
	if err := tx.QueryRowContext(ctx, `SELECT kind,status,plan_type,requires_egress_decision,generation,route_revision
	 FROM accounts WHERE id=?`, req.AccountID).Scan(&kind, &status, &plan, &blocked, &generation, &revision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNoAccounts
		}
		return err
	}
	if blocked || generation != req.AccountGeneration || revision != req.RouteRevision {
		return ErrNoAccounts
	}
	creditOverride := false
	if status == domain.AccountQuotaExceeded && !req.Continuation && kind == domain.AccountChatGPT {
		var err error
		if definition, mapped := domain.AdditionalQuotaForModel(req.Model); mapped && domain.AdditionalQuotaAppliesToPlan(plan, definition) {
			creditOverride, err = additionalQuotaReservationAllowedTx(ctx, tx, req.AccountID, definition.QuotaKey, req.Now)
		} else {
			creditOverride, err = creditBackedReservationAllowedTx(ctx, tx, req.AccountID)
		}
		if err != nil {
			return err
		}
	}
	if status != domain.AccountActive && !creditOverride && !(req.Continuation &&
		(status == domain.AccountRateLimited || status == domain.AccountQuotaExceeded)) {
		return ErrNoAccounts
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM account_credentials WHERE account_id=?`, req.AccountID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNoAccounts
		}
		return err
	}
	if group.Valid && kind == domain.AccountChatGPT {
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM account_group_accounts
		 WHERE group_id=? AND account_id=?`, group.String, req.AccountID).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNoAccounts
			}
			return err
		}
	}
	if accountScoped && !group.Valid {
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM api_key_accounts
 WHERE api_key_id=? AND account_id=?`, req.APIKeyID, req.AccountID).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNoAccounts
			}
			return err
		}
	}
	if sourceScoped && kind == domain.AccountExternal {
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM api_key_sources
 WHERE api_key_id=? AND account_id=?`, req.APIKeyID, req.AccountID).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNoAccounts
			}
			return err
		}
	}
	return nil
}

func additionalQuotaReservationAllowedTx(ctx context.Context, tx *sql.Tx, accountID, quotaKey string, now time.Time) (bool, error) {
	refusalAt, err := loadAccountQuotaRefusalAt(ctx, tx, accountID)
	if err != nil {
		return false, err
	}
	if refusalAt == nil {
		return false, nil
	}
	if now.Before(refusalAt.Add(domain.AdditionalQuotaBlockCooldown)) {
		return false, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT used_percent,reset_at,observed_at
 FROM account_additional_quotas WHERE account_id=? AND quota_key=?`, accountID, quotaKey)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var used float64
		var reset sql.NullInt64
		var observed int64
		if err := rows.Scan(&used, &reset, &observed); err != nil {
			return false, err
		}
		found = true
		if observed <= millis(*refusalAt) || observed < millis(now.Add(-domain.AdditionalQuotaFreshness)) ||
			used >= 100 && (!reset.Valid || reset.Int64 > millis(now)) {
			return false, nil
		}
	}
	return found, rows.Err()
}

func creditBackedReservationAllowedTx(ctx context.Context, tx *sql.Tx, accountID string) (bool, error) {
	credits, err := loadAccountCreditStatus(ctx, tx, accountID)
	if err != nil || credits == nil {
		return false, err
	}
	refusalAt, err := loadAccountQuotaRefusalAt(ctx, tx, accountID)
	return credits.UsableAfter(refusalAt), err
}

func checkAnyReservedAccountTx(ctx context.Context, tx *sql.Tx, keyID string, group sql.NullString, accountScoped, sourceScoped bool) error {
	query := `SELECT count(*) FROM accounts a JOIN account_credentials c ON c.account_id=a.id
 WHERE a.status='active' AND a.requires_egress_decision=0`
	var args []any
	if group.Valid {
		query += ` AND (a.kind!='chatgpt' OR EXISTS(SELECT 1 FROM account_group_accounts g WHERE g.group_id=? AND g.account_id=a.id))`
		args = append(args, group.String)
	}
	if accountScoped && !group.Valid {
		query += ` AND EXISTS(SELECT 1 FROM api_key_accounts ka WHERE ka.api_key_id=? AND ka.account_id=a.id)`
		args = append(args, keyID)
	}
	if sourceScoped {
		query += ` AND (a.kind='chatgpt' OR EXISTS(SELECT 1 FROM api_key_sources ks WHERE ks.api_key_id=? AND ks.account_id=a.id))`
		args = append(args, keyID)
	}
	var count int
	if err := tx.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return ErrNoAccounts
	}
	return nil
}

func (s *Store) GetReservation(ctx context.Context, id string) (domain.Reservation, error) {
	var r domain.Reservation
	var created, updated int64
	err := s.readDB.QueryRowContext(ctx, `SELECT id,api_key_id,account_id,account_generation,route_revision,model,status,
 needs_reconciliation,created_at,updated_at
 FROM usage_reservations WHERE id=?`, id).Scan(&r.ID, &r.APIKeyID, &r.AccountID, &r.AccountGeneration, &r.RouteRevision,
		&r.Model, &r.Status, &r.NeedsReconciliation, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	r.CreatedAt, r.UpdatedAt = fromMillis(created), fromMillis(updated)
	return r, nil
}

func (s *Store) ListReservationsNeedingReconciliation(ctx context.Context, afterID string, limit int) ([]domain.Reservation, error) {
	if limit < 1 || limit > 1000 {
		return nil, ErrInvalid
	}
	rows, err := s.readDB.QueryContext(ctx, `SELECT id FROM usage_reservations
 WHERE needs_reconciliation=1 AND status='reserved' AND id>? ORDER BY id LIMIT ?`, afterID, limit)
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
	result := make([]domain.Reservation, 0, len(ids))
	for _, id := range ids {
		reservation, err := s.GetReservation(ctx, id)
		if err != nil {
			return nil, err
		}
		result = append(result, reservation)
	}
	return result, nil
}

func (s *Store) TouchReservation(ctx context.Context, id string, now time.Time) (bool, error) {
	if now.IsZero() {
		return false, ErrInvalid
	}
	res, err := s.db.ExecContext(ctx, `UPDATE usage_reservations
 SET updated_at=max(updated_at,?) WHERE id=? AND status='reserved'`, millis(now), id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n != 0, err
}

func (s *Store) SettleUsage(ctx context.Context, id string, settlement domain.UsageSettlement) (bool, error) {
	return s.settleUsage(ctx, id, settlement, nil)
}

func (s *Store) settleUsage(ctx context.Context, id string, settlement domain.UsageSettlement, staleBefore *time.Time) (bool, error) {
	if id == "" || (settlement.Status != "finalized" && settlement.Status != "failed" && settlement.Status != "released") {
		return false, ErrInvalid
	}
	if settlement.Status != "released" {
		if err := validateUsageEvent(settlement.Event); err != nil {
			return false, err
		}
	}
	applied := false
	err := s.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var keyID, accountID, status string
		var accountGeneration int64
		var updated int64
		if err := tx.QueryRowContext(ctx, `SELECT api_key_id,account_id,account_generation,status,updated_at
 FROM usage_reservations WHERE id=?`, id).Scan(&keyID, &accountID, &accountGeneration, &status, &updated); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if status != "reserved" || (staleBefore != nil && updated >= millis(*staleBefore)) {
			return nil
		}
		event := settlement.Event
		var deleted, discard bool
		if settlement.Status != "released" {
			if (event.APIKeyID != "" && event.APIKeyID != keyID) ||
				(event.AccountID != "" && accountID != "" && event.AccountID != accountID) ||
				(event.AccountGeneration != 0 && event.AccountGeneration != accountGeneration) {
				return fmt.Errorf("reservation ownership: %w", ErrInvalid)
			}
			event.AccountGeneration = accountGeneration
			event.ReservationID, event.APIKeyID = id, keyID
			if keyID == domain.WarmupKeyID {
				event.APIKeyID, event.RequestKind = "", "warmup"
			}
			if event.AccountID == "" {
				event.AccountID = accountID
			}
			if err := priceUsageEventTx(ctx, tx, &event); err != nil {
				return err
			}
			var policyErr error
			deleted, discard, policyErr = usageDeletionPolicyTx(ctx, tx, event.AccountID, event.AccountGeneration)
			if policyErr != nil {
				return policyErr
			}
			if deleted {
				event.AccountID = ""
			}
		}
		rows, err := tx.QueryContext(ctx, `SELECT limit_id,limit_type,reserved_delta,expected_reset_at
 FROM usage_reservation_items WHERE reservation_id=?`, id)
		if err != nil {
			return err
		}
		type item struct {
			id              int64
			typ             domain.LimitType
			reserved, reset int64
		}
		items := make([]item, 0)
		for rows.Next() {
			var i item
			if err := rows.Scan(&i.id, &i.typ, &i.reserved, &i.reset); err != nil {
				rows.Close()
				return err
			}
			items = append(items, i)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, i := range items {
			actual := int64(0)
			if settlement.Status != "released" {
				actual = event.Usage.ForLimit(i.typ)
			}
			delta := actual - i.reserved
			if delta != 0 {
				if _, err := tx.ExecContext(ctx, `UPDATE api_key_limits
 SET current_value=current_value+? WHERE id=? AND reset_at=? AND current_value+?>=0`,
					delta, i.id, i.reset, delta); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, `UPDATE usage_reservation_items SET actual_delta=?
 WHERE reservation_id=? AND limit_id=?`, actual, id, i.id); err != nil {
				return err
			}
		}
		var input, output, cached, cost any
		if settlement.Status != "released" {
			input, output = event.Usage.InputTokens, event.Usage.OutputTokens
			cached, cost = event.Usage.CachedInputTokens, event.Usage.CostMicrodollars
			if !discard {
				inserted, err := insertUsageTx(ctx, tx, event, deleted)
				if err != nil {
					return err
				}
				if !inserted {
					return fmt.Errorf("request ID already accounted: %w", ErrConflict)
				}
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE usage_reservations SET status=?,input_tokens=?,
	 output_tokens=?,cached_input_tokens=?,cost_microdollars=?,updated_at=?,needs_reconciliation=0
	 WHERE id=? AND status='reserved'`,
			settlement.Status, input, output, cached, cost, time.Now().UTC().UnixMilli(), id); err != nil {
			return err
		}
		if settlement.Status != "released" {
			if _, err := tx.ExecContext(ctx, `UPDATE api_keys SET last_used_at=max(coalesce(last_used_at,0),?) WHERE id=?`,
				millis(event.RequestedAt), keyID); err != nil {
				return err
			}
		}
		applied = true
		return nil
	})
	return applied, err
}

func (s *Store) ReleaseStaleReservations(ctx context.Context, cutoff time.Time) (int, error) {
	if cutoff.IsZero() {
		return 0, ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM usage_reservations
	 WHERE status='reserved' AND needs_reconciliation=0 AND updated_at<?
	 ORDER BY updated_at,id LIMIT 100`, millis(cutoff))
	if err != nil {
		return 0, err
	}
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	count := 0
	for _, id := range ids {
		applied, err := s.settleUsage(ctx, id, domain.UsageSettlement{Status: "released"}, &cutoff)
		if err != nil {
			return count, err
		}
		if applied {
			count++
		}
	}
	return count, nil
}
