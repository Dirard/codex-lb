package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
	"codex-lb/internal/domain/pricing"
)

type pricingEvent struct {
	id, model, tier, kind     string
	account, key, reservation sql.NullString
	legacyID                  sql.NullString
	requested, input, output  int64
	cached, oldCost           int64
	deleted                   bool
}

type pricingScope struct{ name, id string }

type currentCostLimit struct {
	id, start, reset int64
	model            string
	backfillFrom     sql.NullInt64
	backfillUntil    sql.NullInt64
}

func repriceModelHistoryTx(ctx context.Context, tx *sql.Tx, changed string, summary *application.ModelRepriceSummary) error {
	models, err := affectedPriceModelsTx(ctx, tx, changed)
	if err != nil {
		return err
	}
	limits, err := currentCostLimitsTx(ctx, tx)
	if err != nil {
		return err
	}
	totalsDelta := map[pricingScope]int64{}
	limitsDelta := map[int64]int64{}
	for _, model := range models {
		price, priced, err := resolveCodexPriceTx(ctx, tx, model)
		if err != nil {
			return err
		}
		if !priced {
			return fmt.Errorf("affected model has no price: %w", ErrInvalid)
		}
		if err := repriceRawModelTx(ctx, tx, model, price, limits, totalsDelta, limitsDelta, summary); err != nil {
			return err
		}
		var unknown int64
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM usage_events e
		 LEFT JOIN accounts a ON a.id=e.account_id WHERE e.model=?
		 AND (a.id IS NULL OR (a.kind='chatgpt' AND e.request_kind IN ('transcription','reconciled')))
 AND e.model_source_id IS NULL AND (e.legacy_request_id IS NULL OR
 e.requested_at>=coalesce((SELECT hourly_folded_through FROM legacy_import_state WHERE id=1),-9223372036854775808))`, model).Scan(&unknown); err != nil {
			return err
		}
		summary.PartialRawRequests += unknown
	}
	if err := repriceFoldedPriceHistoryTx(ctx, tx, models, limits, totalsDelta, limitsDelta, summary); err != nil {
		return err
	}
	if err := applyPriceDeltasTx(ctx, tx, totalsDelta, limitsDelta); err != nil {
		return err
	}
	return markUndimensionedPriceHistoryTx(ctx, tx, summary)
}

func affectedPriceModelsTx(ctx context.Context, tx *sql.Tx, changed string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT model FROM usage_events UNION SELECT model FROM legacy_hourly_usage`)
	if err != nil {
		return nil, err
	}
	all := []string{}
	for rows.Next() {
		var model string
		if err := rows.Scan(&model); err != nil {
			rows.Close()
			return nil, err
		}
		all = append(all, model)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	canonicalChanged := false
	if canonical, _, err := pricing.LookupCodex(changed); err == nil && canonical == changed {
		canonicalChanged = true
	}
	affected := []string{}
	for _, model := range all {
		normalized := strings.ToLower(model)
		if normalized == changed {
			affected = append(affected, model)
			continue
		}
		if !canonicalChanged {
			continue
		}
		canonical, _, err := pricing.LookupCodex(normalized)
		if err != nil || canonical != changed {
			continue
		}
		_, exact, err := modelPriceOverrideTx(ctx, tx, normalized)
		if err != nil {
			return nil, err
		}
		if !exact {
			affected = append(affected, model)
		}
	}
	return affected, nil
}

func currentCostLimitsTx(ctx context.Context, tx *sql.Tx) (map[string][]currentCostLimit, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,api_key_id,model_filter,limit_window,reset_at,
 backfill_from,backfill_until
 FROM api_key_limits WHERE is_active=1 AND limit_type='cost_usd'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	limits := map[string][]currentCostLimit{}
	for rows.Next() {
		var limit currentCostLimit
		var key string
		var window domain.LimitWindow
		if err := rows.Scan(&limit.id, &key, &limit.model, &window, &limit.reset,
			&limit.backfillFrom, &limit.backfillUntil); err != nil {
			return nil, err
		}
		duration, err := window.Duration()
		if err != nil {
			return nil, err
		}
		limit.start = limit.reset - duration.Milliseconds()
		limits[key] = append(limits[key], limit)
	}
	return limits, rows.Err()
}

func repriceRawModelTx(ctx context.Context, tx *sql.Tx, model string, price pricing.Price,
	limits map[string][]currentCostLimit, totalsDelta map[pricingScope]int64, limitsDelta map[int64]int64,
	summary *application.ModelRepriceSummary) error {
	const batch = 500
	after := ""
	for {
		rows, err := tx.QueryContext(ctx, `SELECT e.request_id,e.model,e.service_tier,e.request_kind,
 e.account_id,e.api_key_id,e.reservation_id,e.legacy_request_id,e.requested_at,
 e.input_tokens,e.output_tokens,e.cached_input_tokens,e.cost_microdollars,e.legacy_deleted
 FROM usage_events e JOIN accounts a ON a.id=e.account_id
 WHERE e.model=? AND e.request_id>? AND a.kind='chatgpt' AND e.model_source_id IS NULL
 AND e.request_kind NOT IN ('transcription','reconciled')
 AND (e.legacy_request_id IS NULL OR e.requested_at>=coalesce(
 (SELECT hourly_folded_through FROM legacy_import_state WHERE id=1),-9223372036854775808))
 ORDER BY e.request_id LIMIT ?`, model, after, batch)
		if err != nil {
			return err
		}
		events := make([]pricingEvent, 0, batch)
		for rows.Next() {
			var event pricingEvent
			if err := rows.Scan(&event.id, &event.model, &event.tier, &event.kind,
				&event.account, &event.key, &event.reservation, &event.legacyID,
				&event.requested, &event.input, &event.output, &event.cached,
				&event.oldCost, &event.deleted); err != nil {
				rows.Close()
				return err
			}
			events = append(events, event)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(events) == 0 {
			return nil
		}
		for _, event := range events {
			if _, err := applyPriceEventTx(ctx, tx, event, price, limits, totalsDelta, limitsDelta, summary, false); err != nil {
				return err
			}
		}
		after = events[len(events)-1].id
	}
}

func applyPriceEventTx(ctx context.Context, tx *sql.Tx, event pricingEvent, price pricing.Price,
	limits map[string][]currentCostLimit, totalsDelta map[pricingScope]int64, limitsDelta map[int64]int64,
	summary *application.ModelRepriceSummary, strict bool) (int64, error) {
	usage := domain.UsageAmount{InputTokens: event.input, OutputTokens: event.output, CachedInputTokens: event.cached}
	if usage.Validate() != nil || event.input == 0 && event.output == 0 && event.oldCost > 0 {
		if strict {
			return 0, fmt.Errorf("covered historical usage is invalid: %w", ErrInvalid)
		}
		summary.PartialRawRequests++
		return 0, nil
	}
	newCost, err := price.Cost(usage, event.tier)
	if err != nil {
		return 0, fmt.Errorf("historical cost exceeds supported range: %w", ErrInvalid)
	}
	summary.RecomputedRequests++
	delta := newCost - event.oldCost
	summary.CostDeltaMicrodollars, err = addPriceDelta(summary.CostDeltaMicrodollars, delta)
	if err != nil || delta == 0 {
		return delta, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE usage_events SET cost_microdollars=? WHERE request_id=?`, newCost, event.id); err != nil {
		return 0, err
	}
	if event.reservation.Valid {
		if _, err := tx.ExecContext(ctx, `UPDATE usage_reservations SET cost_microdollars=? WHERE id=?`,
			newCost, event.reservation.String); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE usage_reservation_items SET actual_delta=?
 WHERE reservation_id=? AND limit_type='cost_usd' AND actual_delta IS NOT NULL`,
			newCost, event.reservation.String); err != nil {
			return 0, err
		}
	}
	if err := addScopeDelta(totalsDelta, pricingScope{"all", "all"}, delta); err != nil {
		return 0, err
	}
	warmup := event.kind == "warmup" || event.kind == "limit_warmup"
	if event.account.Valid {
		counted, err := legacyAccountCostCountedTx(ctx, tx, event, warmup)
		if err != nil {
			return 0, err
		}
		if counted {
			if err := addScopeDelta(totalsDelta, pricingScope{"account", event.account.String}, delta); err != nil {
				return 0, err
			}
		}
	}
	if event.key.Valid && !warmup {
		if err := addScopeDelta(totalsDelta, pricingScope{"key", event.key.String}, delta); err != nil {
			return 0, err
		}
		charged := map[int64]int64{}
		if event.reservation.Valid {
			charged, err = chargedCostLimitsTx(ctx, tx, event.reservation.String)
			if err != nil {
				return 0, err
			}
		}
		for _, limit := range limits[event.key.String] {
			if limit.model != "" && limit.model != event.model {
				continue
			}
			expected, chargedHere := charged[limit.id]
			chargedHere = chargedHere && expected == limit.reset
			backfilled := event.kind == "normal" && limit.backfillFrom.Valid && limit.backfillUntil.Valid &&
				event.requested >= limit.backfillFrom.Int64 && event.requested < limit.backfillUntil.Int64
			if chargedHere || backfilled {
				limitsDelta[limit.id], err = addPriceDelta(limitsDelta[limit.id], delta)
				if err != nil {
					return 0, err
				}
			} else if event.requested >= limit.start && event.requested < limit.reset {
				return 0, fmt.Errorf("current cost-limit charge provenance is unavailable: %w", ErrConflict)
			}
		}
	}
	return delta, nil
}

func chargedCostLimitsTx(ctx context.Context, tx *sql.Tx, reservationID string) (map[int64]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT limit_id,expected_reset_at FROM usage_reservation_items
 WHERE reservation_id=? AND limit_type='cost_usd' AND actual_delta IS NOT NULL`, reservationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	charged := map[int64]int64{}
	for rows.Next() {
		var id, reset int64
		if err := rows.Scan(&id, &reset); err != nil {
			return nil, err
		}
		charged[id] = reset
	}
	return charged, rows.Err()
}

func legacyAccountCostCountedTx(ctx context.Context, tx *sql.Tx, event pricingEvent, warmup bool) (bool, error) {
	if !event.legacyID.Valid {
		return true, nil
	}
	if event.deleted || warmup {
		return false, nil
	}
	var winner string
	err := tx.QueryRowContext(ctx, `SELECT request_id FROM usage_events WHERE account_id=?
 AND legacy_request_id=? AND requested_at=? AND legacy_deleted=0
 AND request_kind NOT IN ('warmup','limit_warmup')
 ORDER BY CAST(substr(request_id,12) AS INTEGER) DESC LIMIT 1`,
		event.account.String, event.legacyID.String, event.requested).Scan(&winner)
	if err != nil {
		return false, err
	}
	return winner == event.id, nil
}

func addScopeDelta(deltas map[pricingScope]int64, scope pricingScope, delta int64) error {
	value, err := addPriceDelta(deltas[scope], delta)
	if err == nil {
		deltas[scope] = value
	}
	return err
}

func addPriceDelta(current, delta int64) (int64, error) {
	if delta > 0 && current > math.MaxInt64-delta || delta < 0 && current < math.MinInt64-delta {
		return 0, fmt.Errorf("cost delta overflows ledger: %w", ErrInvalid)
	}
	return current + delta, nil
}

func applyPriceDeltasTx(ctx context.Context, tx *sql.Tx, totals map[pricingScope]int64, limits map[int64]int64) error {
	for scope, delta := range totals {
		var current int64
		if err := tx.QueryRowContext(ctx, `SELECT cost_microdollars FROM usage_totals
 WHERE scope=? AND scope_id=?`, scope.name, scope.id).Scan(&current); err != nil {
			return fmt.Errorf("missing usage total for reprice: %w", err)
		}
		updated, err := addPriceDelta(current, delta)
		if err != nil || updated < 0 {
			return ErrInvalid
		}
		if _, err := tx.ExecContext(ctx, `UPDATE usage_totals SET cost_microdollars=?
 WHERE scope=? AND scope_id=?`, updated, scope.name, scope.id); err != nil {
			return err
		}
	}
	for id, delta := range limits {
		var current int64
		if err := tx.QueryRowContext(ctx, `SELECT current_value FROM api_key_limits WHERE id=?`, id).Scan(&current); err != nil {
			return err
		}
		updated, err := addPriceDelta(current, delta)
		if err != nil || updated < 0 {
			return ErrInvalid
		}
		if _, err := tx.ExecContext(ctx, `UPDATE api_key_limits SET current_value=? WHERE id=?`, updated, id); err != nil {
			return err
		}
	}
	return nil
}

func markUndimensionedPriceHistoryTx(ctx context.Context, tx *sql.Tx, summary *application.ModelRepriceSummary) error {
	var total, raw, folded int64
	if err := tx.QueryRowContext(ctx, `SELECT coalesce((SELECT request_count FROM usage_totals
 WHERE scope='all' AND scope_id='all'),0)`).Scan(&total); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM usage_events e WHERE
 e.legacy_request_id IS NULL OR e.requested_at>=coalesce(
 (SELECT hourly_folded_through FROM legacy_import_state WHERE id=1),-9223372036854775808)`).Scan(&raw); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT coalesce(sum(request_count),0) FROM legacy_hourly_usage`).Scan(&folded); err != nil {
		return err
	}
	summary.UndimensionedHistory = total > raw+folded
	return nil
}
