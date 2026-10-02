package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"codex-lb/internal/domain"
)

func validateUsageEvent(e domain.UsageEvent) error {
	if e.RequestID == "" || e.Model == "" || e.Status == "" || e.RequestedAt.IsZero() || e.AccountGeneration < 0 ||
		e.QueueLatencyMS < 0 || e.ConnectLatencyMS < 0 || e.FirstEventMS < 0 || e.TotalLatencyMS < 0 {
		return fmt.Errorf("usage event: %w", ErrInvalid)
	}
	if err := e.Usage.Validate(); err != nil {
		return err
	}
	return nil
}

func (s *Store) RecordUsage(ctx context.Context, event domain.UsageEvent) (bool, error) {
	if err := validateUsageEvent(event); err != nil {
		return false, err
	}
	if event.APIKeyID != "" || event.ReservationID != "" {
		return false, fmt.Errorf("metered usage must settle its reservation: %w", ErrInvalid)
	}
	inserted := false
	err := transact(ctx, s.db, func(tx *sql.Tx) error {
		if err := priceUsageEventTx(ctx, tx, &event); err != nil {
			return err
		}
		deleted, discard, err := usageDeletionPolicyTx(ctx, tx, event.AccountID, event.AccountGeneration)
		if err != nil || discard {
			return err
		}
		if deleted {
			event.AccountID = ""
		}
		inserted, err = insertUsageTx(ctx, tx, event, deleted)
		return err
	})
	return inserted, err
}

func insertUsageTx(ctx context.Context, tx *sql.Tx, e domain.UsageEvent, deleted bool) (bool, error) {
	if e.RequestKind == "" {
		e.RequestKind = "normal"
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO usage_events (
 request_id,reservation_id,api_key_id,account_id,account_generation,model_source_id,model,service_tier,
 request_kind,status,error_code,requested_at,queue_latency_ms,connect_latency_ms,
 first_event_ms,total_latency_ms,input_tokens,output_tokens,cached_input_tokens,
 reasoning_tokens,cost_microdollars,conversation_id,useragent,useragent_group,
 client_ip,reasoning_effort,plan_type,source,transport,latency_first_token_ms,
 reasoning_tokens_known,legacy_deleted
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
 ON CONFLICT(request_id) DO NOTHING`,
		e.RequestID, nullIfEmpty(e.ReservationID), nullIfEmpty(e.APIKeyID), nullIfEmpty(e.AccountID), e.AccountGeneration,
		nullIfEmpty(e.ModelSourceID), e.Model, e.ServiceTier, e.RequestKind, e.Status, e.ErrorCode,
		millis(e.RequestedAt), e.QueueLatencyMS, e.ConnectLatencyMS, e.FirstEventMS, e.TotalLatencyMS,
		e.Usage.InputTokens, e.Usage.OutputTokens, e.Usage.CachedInputTokens,
		e.Usage.ReasoningTokens, e.Usage.CostMicrodollars,
		conversationValue(e.ConversationID), metadataValue(e.UserAgent, 1024),
		metadataValue(e.UserAgentGroup, 128), metadataValue(e.ClientIP, 128),
		metadataValue(e.ReasoningEffort, 32), metadataValue(e.PlanType, 64),
		metadataValue(e.Source, 128), metadataValue(e.Transport, 32),
		positiveOrNull(e.FirstTokenMS), boolInt(e.ReasoningTokensKnown), boolInt(deleted))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	if err := incrementTotalsTx(ctx, tx, "all", "all", e); err != nil {
		return false, err
	}
	if e.AccountID != "" {
		if err := incrementTotalsTx(ctx, tx, "account", e.AccountID, e); err != nil {
			return false, err
		}
	}
	if e.APIKeyID != "" && e.RequestKind != "warmup" && e.RequestKind != "limit_warmup" {
		if err := incrementTotalsTx(ctx, tx, "key", e.APIKeyID, e); err != nil {
			return false, err
		}
	}
	return true, nil
}

func incrementTotalsTx(ctx context.Context, tx *sql.Tx, scope, id string, e domain.UsageEvent) error {
	failed := 0
	if e.Status != "success" && e.Status != "completed" {
		failed = 1
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO usage_totals
 (scope,scope_id,request_count,failed_count,input_tokens,output_tokens,cached_input_tokens,
 reasoning_tokens,cost_microdollars) VALUES (?,?,1,?,?,?,?,?,?)
 ON CONFLICT(scope,scope_id) DO UPDATE SET
 request_count=request_count+1,failed_count=failed_count+excluded.failed_count,
 input_tokens=input_tokens+excluded.input_tokens,output_tokens=output_tokens+excluded.output_tokens,
 cached_input_tokens=cached_input_tokens+excluded.cached_input_tokens,
 reasoning_tokens=reasoning_tokens+excluded.reasoning_tokens,
 cost_microdollars=cost_microdollars+excluded.cost_microdollars`,
		scope, id, failed, e.Usage.InputTokens, e.Usage.OutputTokens,
		e.Usage.CachedInputTokens, e.Usage.ReasoningTokens, e.Usage.CostMicrodollars)
	return err
}

func nullIfEmpty(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func conversationValue(v string) any {
	if len(v) > 256 {
		return nil
	}
	return nullIfEmpty(v)
}

func metadataValue(v string, maxBytes int) any {
	if v == "" {
		return nil
	}
	if len(v) > maxBytes {
		v = strings.ToValidUTF8(v[:maxBytes], "")
	}
	return v
}

func positiveOrNull(v int64) any {
	if v <= 0 {
		return nil
	}
	return v
}

func (s *Store) UsageTotals(ctx context.Context, keyID, accountID string) (domain.UsageTotals, error) {
	var totals domain.UsageTotals
	if keyID != "" && accountID != "" {
		return s.pairUsageTotals(ctx, keyID, accountID)
	}
	var query string
	var args []any
	scope, id := "all", "all"
	if keyID != "" {
		scope, id = "key", keyID
	} else if accountID != "" {
		scope, id = "account", accountID
	}
	query = `SELECT request_count,failed_count,input_tokens,output_tokens,cached_input_tokens,
 reasoning_tokens,cost_microdollars FROM usage_totals WHERE scope=? AND scope_id=?`
	args = []any{scope, id}
	err := s.readDB.QueryRowContext(ctx, query, args...).Scan(&totals.RequestCount, &totals.FailedCount,
		&totals.Usage.InputTokens, &totals.Usage.OutputTokens, &totals.Usage.CachedInputTokens,
		&totals.Usage.ReasoningTokens, &totals.Usage.CostMicrodollars)
	if errors.Is(err, sql.ErrNoRows) {
		return totals, nil
	}
	if totals.Usage.CachedInputTokens > totals.Usage.InputTokens {
		totals.Usage.CachedInputTokens = totals.Usage.InputTokens
	}
	return totals, err
}

func (s *Store) pairUsageTotals(ctx context.Context, keyID, accountID string) (domain.UsageTotals, error) {
	var totals domain.UsageTotals
	tx, err := s.readDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return totals, err
	}
	defer tx.Rollback()
	var watermark sql.NullInt64
	err = tx.QueryRowContext(ctx, "SELECT hourly_folded_through FROM legacy_import_state WHERE id=1").Scan(&watermark)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return totals, err
	}
	query := `SELECT count(*),coalesce(sum(status NOT IN ('success','completed')),0),
 coalesce(sum(input_tokens),0),coalesce(sum(output_tokens),0),coalesce(sum(cached_input_tokens),0),
 coalesce(sum(reasoning_tokens),0),coalesce(sum(cost_microdollars),0)
 FROM usage_events WHERE api_key_id=? AND account_id=?
 AND request_kind NOT IN ('warmup','limit_warmup')`
	args := []any{keyID, accountID}
	if watermark.Valid {
		query += " AND (legacy_request_id IS NULL OR requested_at>=?)"
		args = append(args, watermark.Int64)
	}
	err = tx.QueryRowContext(ctx, query, args...).Scan(&totals.RequestCount, &totals.FailedCount,
		&totals.Usage.InputTokens, &totals.Usage.OutputTokens, &totals.Usage.CachedInputTokens,
		&totals.Usage.ReasoningTokens, &totals.Usage.CostMicrodollars)
	if err != nil {
		return totals, err
	}
	var folded domain.UsageTotals
	var costUSD float64
	err = tx.QueryRowContext(ctx, `SELECT coalesce(sum(request_count),0),
 coalesce(sum(error_count+cancelled_count),0),coalesce(sum(input_tokens),0),
 coalesce(sum(output_or_reasoning_tokens),0),coalesce(sum(cached_input_tokens),0),
 coalesce(sum(reasoning_tokens),0),coalesce(sum(cost_usd),0)
 FROM legacy_hourly_usage WHERE api_key_id=? AND account_id=?
 AND request_kind NOT IN ('warmup','limit_warmup')`, toLegacyDimension(keyID),
		toLegacyDimension(accountID)).Scan(&folded.RequestCount,
		&folded.FailedCount, &folded.Usage.InputTokens, &folded.Usage.OutputTokens,
		&folded.Usage.CachedInputTokens, &folded.Usage.ReasoningTokens, &costUSD)
	if err != nil {
		return totals, err
	}
	folded.Usage.CostMicrodollars, err = legacyCostMicro(sql.NullFloat64{Float64: costUSD, Valid: true})
	if err != nil {
		return totals, err
	}
	totals.RequestCount += folded.RequestCount
	totals.FailedCount += folded.FailedCount
	totals.Usage.InputTokens += folded.Usage.InputTokens
	totals.Usage.OutputTokens += folded.Usage.OutputTokens
	totals.Usage.CachedInputTokens += folded.Usage.CachedInputTokens
	totals.Usage.ReasoningTokens += folded.Usage.ReasoningTokens
	totals.Usage.CostMicrodollars += folded.Usage.CostMicrodollars
	if totals.Usage.CachedInputTokens > totals.Usage.InputTokens {
		totals.Usage.CachedInputTokens = totals.Usage.InputTokens
	}
	return totals, nil
}

func toLegacyDimension(v string) string {
	if v == "" {
		return "\x1f"
	}
	if v[0] == '\x1f' {
		return "\x1f" + v
	}
	return v
}
