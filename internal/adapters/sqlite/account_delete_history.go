package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// usageDeletionPolicyTx binds a late bill to the incarnation that admitted it.
// The account can already be reimported with a newer generation.
func usageDeletionPolicyTx(ctx context.Context, tx *sql.Tx, accountID string, generation int64) (deleted, discard bool, err error) {
	if accountID == "" {
		return false, false, nil
	}
	err = tx.QueryRowContext(ctx, `SELECT delete_history FROM account_deletions
 WHERE account_id=? AND generation=?`, accountID, generation).Scan(&discard)
	if err == nil {
		return true, discard, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, false, err
	}
	var current int64
	err = tx.QueryRowContext(ctx, `SELECT generation FROM accounts WHERE id=?`, accountID).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil // Legacy unbound bills may name an account no longer present.
	}
	if err != nil {
		return false, false, err
	}
	if current != generation {
		return false, false, fmt.Errorf("stale account incarnation: %w", ErrConflict)
	}
	return false, false, nil
}

// CleanupDeletedAccounts consumes at most limit history rows in one short
// transaction. Its policy record survives restart and is never removed.
func (s *Store) CleanupDeletedAccounts(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		return 0, ErrInvalid
	}
	count := 0
	err := transact(ctx, s.db, func(tx *sql.Tx) error {
		var watermark sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT hourly_folded_through FROM legacy_import_state WHERE id=1`).Scan(&watermark)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		for count < limit {
			var accountID string
			var generation int64
			var discard bool
			err := tx.QueryRowContext(ctx, `SELECT account_id,generation,delete_history
 FROM account_deletions WHERE cleanup_done=0 ORDER BY account_id,generation LIMIT 1`).
				Scan(&accountID, &generation, &discard)
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			// A deleted account's lifetime total must not become the next incarnation's.
			if _, err := tx.ExecContext(ctx, `DELETE FROM usage_totals WHERE scope='account' AND scope_id=?`, accountID); err != nil {
				return err
			}
			steps := []func(context.Context, *sql.Tx, string, int64, bool, sql.NullInt64, int) (int, error){
				cleanupDeletedRawTx, cleanupDeletedFoldedTx, cleanupDeletedErrorsTx,
				cleanupDeletedConversationsTx, cleanupDeletedArchivesTx, cleanupDeletedQuotaHistoryTx,
			}
			complete := true
			for _, step := range steps {
				n, err := step(ctx, tx, accountID, generation, discard, watermark, limit-count)
				if err != nil {
					return err
				}
				count += n
				if count == limit {
					complete = false // An exact fit is checked on the next batch.
					break
				}
			}
			if complete {
				if _, err := tx.ExecContext(ctx, `UPDATE account_deletions SET cleanup_done=1
 WHERE account_id=? AND generation=?`, accountID, generation); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return count, err
}

type deletedRaw struct {
	rowID, requested, input, output, cached, reasoning, cost int64
	key, legacyID                                            sql.NullString
	kind, status                                             string
}

func cleanupDeletedRawTx(ctx context.Context, tx *sql.Tx, accountID string, generation int64, discard bool, watermark sql.NullInt64, limit int) (int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT rowid,api_key_id,legacy_request_id,request_kind,status,
 requested_at,input_tokens,output_tokens,cached_input_tokens,reasoning_tokens,cost_microdollars
 FROM usage_events WHERE account_id=? AND account_generation=? ORDER BY rowid LIMIT ?`, accountID, generation, limit)
	if err != nil {
		return 0, err
	}
	items := make([]deletedRaw, 0, limit)
	for rows.Next() {
		var e deletedRaw
		if err := rows.Scan(&e.rowID, &e.key, &e.legacyID, &e.kind, &e.status, &e.requested,
			&e.input, &e.output, &e.cached, &e.reasoning, &e.cost); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	for _, e := range items {
		if discard {
			if !e.legacyID.Valid || !watermark.Valid || e.requested >= watermark.Int64 {
				failed := int64(0)
				if e.status != "success" && e.status != "completed" {
					failed = 1
				}
				if err := subtractDeletedTotalTx(ctx, tx, "all", "all", 1, failed, e.input, e.output, e.cached, e.reasoning, e.cost); err != nil {
					return 0, err
				}
				if e.key.Valid && e.kind != "warmup" && e.kind != "limit_warmup" {
					if err := subtractDeletedTotalTx(ctx, tx, "key", e.key.String, 1, failed, e.input, e.output, e.cached, e.reasoning, e.cost); err != nil {
						return 0, err
					}
				}
			}
			_, err = tx.ExecContext(ctx, `DELETE FROM usage_events WHERE rowid=?`, e.rowID)
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE usage_events SET account_id=NULL,legacy_deleted=1 WHERE rowid=?`, e.rowID)
		}
		if err != nil {
			return 0, err
		}
	}
	return len(items), nil
}

type deletedFolded struct {
	rowID, requests, failed, input, output, cached, reasoning int64
	key, kind                                                 string
	cost                                                      float64
}

func cleanupDeletedFoldedTx(ctx context.Context, tx *sql.Tx, accountID string, _ int64, discard bool, _ sql.NullInt64, limit int) (int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT rowid,api_key_id,request_kind,request_count,
 error_count+cancelled_count,input_tokens,output_or_reasoning_tokens,cached_input_tokens,
 reasoning_tokens,cost_usd FROM legacy_hourly_usage WHERE account_id=? ORDER BY rowid LIMIT ?`, toLegacyDimension(accountID), limit)
	if err != nil {
		return 0, err
	}
	items := make([]deletedFolded, 0, limit)
	for rows.Next() {
		var h deletedFolded
		if err := rows.Scan(&h.rowID, &h.key, &h.kind, &h.requests, &h.failed, &h.input,
			&h.output, &h.cached, &h.reasoning, &h.cost); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, h)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	for _, h := range items {
		if discard {
			cost, err := legacyCostMicro(sql.NullFloat64{Float64: h.cost, Valid: true})
			if err != nil {
				return 0, err
			}
			if err := subtractDeletedTotalTx(ctx, tx, "all", "all", h.requests, h.failed, h.input, h.output, h.cached, h.reasoning, cost); err != nil {
				return 0, err
			}
			if h.key != "\x1f" && h.kind != "warmup" && h.kind != "limit_warmup" {
				if err := subtractDeletedTotalTx(ctx, tx, "key", fromLegacyDimension(h.key), h.requests, h.failed, h.input, h.output, h.cached, h.reasoning, cost); err != nil {
					return 0, err
				}
			}
		} else if _, err := tx.ExecContext(ctx, `INSERT INTO legacy_hourly_usage
 (bucket_epoch,account_id,api_key_id,model,service_tier,request_kind,is_deleted,
 request_count,error_count,cancelled_count,input_tokens,output_tokens,reasoning_tokens,
 output_or_reasoning_tokens,cached_input_tokens,cached_input_tokens_clamped,cost_usd,
 cost_count,reasoning_known_count)
 SELECT bucket_epoch,char(31),api_key_id,model,service_tier,request_kind,1,
 request_count,error_count,cancelled_count,input_tokens,output_tokens,reasoning_tokens,
 output_or_reasoning_tokens,cached_input_tokens,cached_input_tokens_clamped,cost_usd,
 cost_count,reasoning_known_count FROM legacy_hourly_usage WHERE rowid=?
 ON CONFLICT(bucket_epoch,account_id,api_key_id,model,service_tier,request_kind,is_deleted)
 DO UPDATE SET request_count=request_count+excluded.request_count,
 error_count=error_count+excluded.error_count,cancelled_count=cancelled_count+excluded.cancelled_count,
 input_tokens=input_tokens+excluded.input_tokens,output_tokens=output_tokens+excluded.output_tokens,
 reasoning_tokens=reasoning_tokens+excluded.reasoning_tokens,
 output_or_reasoning_tokens=output_or_reasoning_tokens+excluded.output_or_reasoning_tokens,
 cached_input_tokens=cached_input_tokens+excluded.cached_input_tokens,
 cached_input_tokens_clamped=cached_input_tokens_clamped+excluded.cached_input_tokens_clamped,
 cost_usd=cost_usd+excluded.cost_usd,cost_count=cost_count+excluded.cost_count,
 reasoning_known_count=reasoning_known_count+excluded.reasoning_known_count`, h.rowID); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM legacy_hourly_usage WHERE rowid=?`, h.rowID); err != nil {
			return 0, err
		}
	}
	return len(items), nil
}

func cleanupDeletedErrorsTx(ctx context.Context, tx *sql.Tx, accountID string, _ int64, discard bool, _ sql.NullInt64, limit int) (int, error) {
	if !discard {
		if _, err := tx.ExecContext(ctx, `INSERT INTO legacy_hourly_errors
 (bucket_epoch,account_id,error_code,error_count)
 SELECT bucket_epoch,char(31),error_code,error_count FROM legacy_hourly_errors
 WHERE rowid IN (SELECT rowid FROM legacy_hourly_errors WHERE account_id=? ORDER BY rowid LIMIT ?)
 ON CONFLICT(bucket_epoch,account_id,error_code) DO UPDATE SET error_count=error_count+excluded.error_count`, toLegacyDimension(accountID), limit); err != nil {
			return 0, err
		}
	}
	return deleteCountTx(ctx, tx, `DELETE FROM legacy_hourly_errors WHERE rowid IN
 (SELECT rowid FROM legacy_hourly_errors WHERE account_id=? ORDER BY rowid LIMIT ?)`, toLegacyDimension(accountID), limit)
}

func cleanupDeletedConversationsTx(ctx context.Context, tx *sql.Tx, accountID string, _ int64, discard bool, _ sql.NullInt64, limit int) (int, error) {
	if !discard {
		if _, err := tx.ExecContext(ctx, `INSERT INTO legacy_conversation_hourly
 (bucket_epoch,conversation_id,account_id,is_deleted,request_count)
 SELECT bucket_epoch,conversation_id,char(31),1,request_count FROM legacy_conversation_hourly
 WHERE rowid IN (SELECT rowid FROM legacy_conversation_hourly WHERE account_id=? ORDER BY rowid LIMIT ?)
 ON CONFLICT(bucket_epoch,conversation_id,account_id,is_deleted) DO UPDATE
 SET request_count=request_count+excluded.request_count`, toLegacyDimension(accountID), limit); err != nil {
			return 0, err
		}
	}
	return deleteCountTx(ctx, tx, `DELETE FROM legacy_conversation_hourly WHERE rowid IN
 (SELECT rowid FROM legacy_conversation_hourly WHERE account_id=? ORDER BY rowid LIMIT ?)`, toLegacyDimension(accountID), limit)
}

func cleanupDeletedArchivesTx(ctx context.Context, tx *sql.Tx, accountID string, _ int64, discard bool, _ sql.NullInt64, limit int) (int, error) {
	if discard {
		return deleteCountTx(ctx, tx, `DELETE FROM error_archives WHERE rowid IN
 (SELECT rowid FROM error_archives WHERE account_id=? ORDER BY rowid LIMIT ?)`, accountID, limit)
	}
	return deleteCountTx(ctx, tx, `UPDATE error_archives SET account_id='' WHERE rowid IN
 (SELECT rowid FROM error_archives WHERE account_id=? ORDER BY rowid LIMIT ?)`, accountID, limit)
}

func cleanupDeletedQuotaHistoryTx(ctx context.Context, tx *sql.Tx, accountID string, _ int64, _ bool, _ sql.NullInt64, limit int) (int, error) {
	return deleteCountTx(ctx, tx, `DELETE FROM account_quota_history WHERE id IN
 (SELECT id FROM account_quota_history WHERE account_id=? ORDER BY id LIMIT ?)`, accountID, limit)
}

func deleteCountTx(ctx context.Context, tx *sql.Tx, query string, args ...any) (int, error) {
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	n, err := result.RowsAffected()
	return int(n), err
}

func subtractDeletedTotalTx(ctx context.Context, tx *sql.Tx, scope, id string, count, failed, input, output, cached, reasoning, cost int64) error {
	res, err := tx.ExecContext(ctx, `UPDATE usage_totals SET request_count=request_count-?,
 failed_count=failed_count-?,input_tokens=input_tokens-?,output_tokens=output_tokens-?,
 cached_input_tokens=cached_input_tokens-?,reasoning_tokens=reasoning_tokens-?,
 cost_microdollars=cost_microdollars-? WHERE scope=? AND scope_id=?
 AND request_count>=? AND failed_count>=? AND input_tokens>=? AND output_tokens>=?
 AND cached_input_tokens>=? AND reasoning_tokens>=? AND cost_microdollars>=?`,
		count, failed, input, output, cached, reasoning, cost, scope, id,
		count, failed, input, output, cached, reasoning, cost)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("usage total attribution underflow (%s): %w", scope, ErrConflict)
	}
	return nil
}
