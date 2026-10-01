package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

type retainedEvent struct {
	id, model, tier, kind, status                     string
	account, key, errorCode, conversation, legacyID   sql.NullString
	requested, input, output, reasoning, cached, cost int64
	known, deleted                                    bool
}

// FoldAndPruneRequestLogs removes only event detail after atomically folding
// its content-free measures. Lifetime usage_totals are intentionally untouched.
func (s *Store) FoldAndPruneRequestLogs(ctx context.Context, cutoff time.Time, limit int) (int, error) {
	if cutoff.IsZero() || limit < 1 || limit > 1000 {
		return 0, ErrInvalid
	}
	count := 0
	err := transact(ctx, s.db, func(tx *sql.Tx) error {
		var importWatermark sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT hourly_folded_through FROM legacy_import_state WHERE id=1`).Scan(&importWatermark)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT request_id,account_id,api_key_id,model,service_tier,
 request_kind,status,error_code,conversation_id,legacy_request_id,requested_at,input_tokens,
 output_tokens,reasoning_tokens,cached_input_tokens,cost_microdollars,reasoning_tokens_known,
 legacy_deleted FROM usage_events WHERE requested_at<? ORDER BY requested_at,request_id LIMIT ?`,
			millis(cutoff), limit)
		if err != nil {
			return err
		}
		items := make([]retainedEvent, 0, limit)
		for rows.Next() {
			var e retainedEvent
			if err := rows.Scan(&e.id, &e.account, &e.key, &e.model, &e.tier, &e.kind, &e.status,
				&e.errorCode, &e.conversation, &e.legacyID, &e.requested, &e.input, &e.output,
				&e.reasoning, &e.cached, &e.cost, &e.known, &e.deleted); err != nil {
				rows.Close()
				return err
			}
			items = append(items, e)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, e := range items {
			alreadyFolded := e.legacyID.Valid && importWatermark.Valid && e.requested < importWatermark.Int64
			if !alreadyFolded {
				if err := foldReportEvent(ctx, tx, e); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM usage_events WHERE request_id=?", e.id); err != nil {
				return err
			}
			count++
		}
		return nil
	})
	return count, err
}

func foldReportEvent(ctx context.Context, tx *sql.Tx, e retainedEvent) error {
	bucket := e.requested / 3600000 * 3600
	account, key := toLegacyDimension(textVal(e.account)), toLegacyDimension(textVal(e.key))
	tier := e.tier
	errorCount, cancelled := 0, 0
	if e.status == "cancelled" {
		cancelled = 1
	} else if e.status != "success" && e.status != "completed" {
		errorCount = 1
	}
	known := boolInt(e.known)
	clamped := e.cached
	if clamped > e.input {
		clamped = e.input
	}
	if clamped < 0 {
		clamped = 0
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO legacy_hourly_usage
 (bucket_epoch,account_id,api_key_id,model,service_tier,request_kind,is_deleted,
 request_count,error_count,cancelled_count,input_tokens,output_tokens,reasoning_tokens,
 output_or_reasoning_tokens,cached_input_tokens,cached_input_tokens_clamped,cost_usd,
 cost_count,reasoning_known_count)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
 ON CONFLICT(bucket_epoch,account_id,api_key_id,model,service_tier,request_kind,is_deleted)
 DO UPDATE SET request_count=request_count+1,error_count=error_count+excluded.error_count,
 cancelled_count=cancelled_count+excluded.cancelled_count,
 input_tokens=input_tokens+excluded.input_tokens,output_tokens=output_tokens+excluded.output_tokens,
 reasoning_tokens=reasoning_tokens+excluded.reasoning_tokens,
 output_or_reasoning_tokens=output_or_reasoning_tokens+excluded.output_or_reasoning_tokens,
 cached_input_tokens=cached_input_tokens+excluded.cached_input_tokens,
 cached_input_tokens_clamped=cached_input_tokens_clamped+excluded.cached_input_tokens_clamped,
 cost_usd=cost_usd+excluded.cost_usd,cost_count=cost_count+1,
 reasoning_known_count=reasoning_known_count+excluded.reasoning_known_count`,
		bucket, account, key, e.model, tier, e.kind, boolInt(e.deleted), 1, errorCount, cancelled,
		e.input, e.output, e.reasoning, e.output, e.cached, clamped, float64(e.cost)/1e6, 1, known)
	if err != nil {
		return err
	}
	if e.kind == "warmup" || e.kind == "limit_warmup" {
		return nil
	}
	if errorCount > 0 && e.errorCode.Valid && e.errorCode.String != "" {
		if _, err := tx.ExecContext(ctx, `INSERT INTO legacy_hourly_errors
 (bucket_epoch,account_id,error_code,error_count) VALUES(?,?,?,1)
 ON CONFLICT(bucket_epoch,account_id,error_code) DO UPDATE SET error_count=error_count+1`,
			bucket, account, e.errorCode.String); err != nil {
			return err
		}
	}
	if e.conversation.Valid {
		conversation := strings.TrimSpace(e.conversation.String)
		if conversation != "" {
			if _, err := tx.ExecContext(ctx, `INSERT INTO legacy_conversation_hourly
 (bucket_epoch,conversation_id,account_id,is_deleted,request_count) VALUES(?,?,?,?,1)
 ON CONFLICT(bucket_epoch,conversation_id,account_id,is_deleted)
 DO UPDATE SET request_count=request_count+1`,
				bucket, conversation, account, boolInt(e.deleted)); err != nil {
				return err
			}
		}
	}
	return nil
}
