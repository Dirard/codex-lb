package sqlite

import (
	"context"
	"database/sql"
	"errors"
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

// Repricing chunk sizes bound one batch-writer job.
const (
	repriceRawChunk    = 500
	repriceBucketChunk = 100
)

type priceResolver func(context.Context, *sql.Tx, string) (pricing.Price, bool, error)

// checkModelReprice dry-runs the whole history repricing on one read
// snapshot with the prospective tariff, writing nothing.
func (s *Store) checkModelReprice(ctx context.Context, changed string, override *pricing.Price) error {
	tx, err := s.readDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, existed, err := modelPriceOverrideTx(ctx, tx, changed)
	if err != nil || override == nil && !existed {
		return err
	}
	resolve := func(ctx context.Context, tx *sql.Tx, model string) (pricing.Price, bool, error) {
		return prospectiveCodexPriceTx(ctx, tx, changed, override, model)
	}
	if _, priced, err := resolve(ctx, tx, changed); err != nil || !priced {
		return err
	}
	models, err := affectedPriceModelsTx(ctx, tx, changed)
	if err != nil {
		return err
	}
	limits, err := currentCostLimitsTx(ctx, tx)
	if err != nil {
		return err
	}
	var summary application.ModelRepriceSummary
	totals, limitDeltas := map[pricingScope]int64{}, map[int64]int64{}
	for _, model := range models {
		price, priced, err := resolve(ctx, tx, model)
		if err != nil {
			return err
		}
		if !priced {
			return fmt.Errorf("affected model has no price: %w", ErrInvalid)
		}
		for after, done := "", false; !done; {
			if after, done, err = repriceRawChunkTx(ctx, tx, model, after, price, limits, totals, limitDeltas, &summary, false); err != nil {
				return err
			}
		}
		for after, done := (foldedCursor{}), false; !done; {
			if after, done, err = repriceFoldedChunkTx(ctx, tx, model, after, resolve, limits, totals, limitDeltas, &summary, false); err != nil {
				return err
			}
		}
	}
	return applyPriceDeltasTx(ctx, tx, totals, limitDeltas, false)
}

// repriceModelHistory reprices the saved tariff's history in bounded write
// jobs. Each job applies its event costs with their aggregate and current-limit
// deltas, so the ledger stays consistent between chunks and a repeated pass
// changes nothing already repriced. The pending marker is cleared only for the
// change that started this pass.
func (s *Store) repriceModelHistory(ctx context.Context, changed string, requestedAt int64, summary *application.ModelRepriceSummary) error {
	var models []string
	if err := s.readSnapshot(ctx, func(tx *sql.Tx) (err error) {
		models, err = affectedPriceModelsTx(ctx, tx, changed)
		return err
	}); err != nil {
		return err
	}
	chunk := func(run func(context.Context, *sql.Tx, map[string][]currentCostLimit, map[pricingScope]int64, map[int64]int64, *application.ModelRepriceSummary) error) error {
		return s.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
			limits, err := currentCostLimitsTx(ctx, tx)
			if err != nil {
				return err
			}
			partial := *summary
			totals, limitDeltas := map[pricingScope]int64{}, map[int64]int64{}
			if err := run(ctx, tx, limits, totals, limitDeltas, &partial); err != nil {
				return err
			}
			if err := applyPriceDeltasTx(ctx, tx, totals, limitDeltas, true); err != nil {
				return err
			}
			*summary = partial
			return nil
		})
	}
	for _, model := range models {
		for after, done := "", false; !done; {
			err := chunk(func(ctx context.Context, tx *sql.Tx, limits map[string][]currentCostLimit, totals map[pricingScope]int64, limitDeltas map[int64]int64, partial *application.ModelRepriceSummary) error {
				price, priced, err := resolveCodexPriceTx(ctx, tx, model)
				if err != nil {
					return err
				}
				if !priced {
					return fmt.Errorf("affected model has no price: %w", ErrInvalid)
				}
				after, done, err = repriceRawChunkTx(ctx, tx, model, after, price, limits, totals, limitDeltas, partial, true)
				return err
			})
			if err != nil {
				return err
			}
		}
		if err := s.readSnapshot(ctx, func(tx *sql.Tx) error {
			unknown, err := unknownRawPriceRequestsTx(ctx, tx, model)
			summary.PartialRawRequests += unknown
			return err
		}); err != nil {
			return err
		}
	}
	for _, model := range models {
		for after, done := (foldedCursor{}), false; !done; {
			err := chunk(func(ctx context.Context, tx *sql.Tx, limits map[string][]currentCostLimit, totals map[pricingScope]int64, limitDeltas map[int64]int64, partial *application.ModelRepriceSummary) (err error) {
				after, done, err = repriceFoldedChunkTx(ctx, tx, model, after, resolveCodexPriceTx, limits, totals, limitDeltas, partial, true)
				return err
			})
			if err != nil {
				return err
			}
		}
	}
	if err := s.readSnapshot(ctx, func(tx *sql.Tx) error { return markUndimensionedPriceHistoryTx(ctx, tx, summary) }); err != nil {
		return err
	}
	_, err := s.exec(ctx, `DELETE FROM model_reprice_pending WHERE model=? AND requested_at=?`, changed, requestedAt)
	return err
}

// readSnapshot runs fn in one read transaction of the read pool.
func (s *Store) readSnapshot(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.readDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return fn(tx)
}

// ResumeModelReprices finishes repricing interrupted by a restart and returns
// when every pending change is done, one fails or ctx is cancelled; unfinished
// work stays pending for the next call. It belongs to the process that runs
// background work. Should another store on the same database resume the same
// change concurrently, each chunk still reads the stored costs and applies
// their deltas inside one write transaction, which IMMEDIATE serializes, so a
// chunk already repriced yields no further delta.
func (s *Store) ResumeModelReprices(ctx context.Context) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("model reprice resumption failed")
		}
	}()
	type pending struct {
		model       string
		requestedAt int64
	}
	var work []pending
	err = s.readSnapshot(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT model,requested_at FROM model_reprice_pending ORDER BY requested_at,model`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item pending
			if err := rows.Scan(&item.model, &item.requestedAt); err != nil {
				return err
			}
			work = append(work, item)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	for _, item := range work {
		var summary application.ModelRepriceSummary
		if err := s.repriceModelHistory(ctx, item.model, item.requestedAt, &summary); err != nil {
			return err
		}
	}
	return nil
}

func unknownRawPriceRequestsTx(ctx context.Context, tx *sql.Tx, model string) (int64, error) {
	var unknown int64
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM usage_events e
	 LEFT JOIN accounts a ON a.id=e.account_id WHERE e.model=?
	 AND (a.id IS NULL OR (a.kind='chatgpt' AND e.request_kind IN ('transcription','reconciled')))
 AND e.model_source_id IS NULL AND (e.legacy_request_id IS NULL OR
 e.requested_at>=coalesce((SELECT hourly_folded_through FROM legacy_import_state WHERE id=1),-9223372036854775808))`, model).Scan(&unknown)
	return unknown, err
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

// repriceRawChunkTx reprices at most repriceRawChunk raw events after the
// request ID cursor and reports the next cursor and whether the model is done.
func repriceRawChunkTx(ctx context.Context, tx *sql.Tx, model, after string, price pricing.Price,
	limits map[string][]currentCostLimit, totalsDelta map[pricingScope]int64, limitsDelta map[int64]int64,
	summary *application.ModelRepriceSummary, write bool) (string, bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT e.request_id,e.model,e.service_tier,e.request_kind,
 e.account_id,e.api_key_id,e.reservation_id,e.legacy_request_id,e.requested_at,
 e.input_tokens,e.output_tokens,e.cached_input_tokens,e.cost_microdollars,e.legacy_deleted
 FROM usage_events e JOIN accounts a ON a.id=e.account_id
 WHERE e.model=? AND e.request_id>? AND a.kind='chatgpt' AND e.model_source_id IS NULL
 AND e.request_kind NOT IN ('transcription','reconciled')
 AND (e.legacy_request_id IS NULL OR e.requested_at>=coalesce(
 (SELECT hourly_folded_through FROM legacy_import_state WHERE id=1),-9223372036854775808))
 ORDER BY e.request_id LIMIT ?`, model, after, repriceRawChunk)
	if err != nil {
		return after, false, err
	}
	events := make([]pricingEvent, 0, repriceRawChunk)
	for rows.Next() {
		var event pricingEvent
		if err := rows.Scan(&event.id, &event.model, &event.tier, &event.kind,
			&event.account, &event.key, &event.reservation, &event.legacyID,
			&event.requested, &event.input, &event.output, &event.cached,
			&event.oldCost, &event.deleted); err != nil {
			rows.Close()
			return after, false, err
		}
		events = append(events, event)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return after, false, err
	}
	for _, event := range events {
		if _, err := applyPriceEventTx(ctx, tx, event, price, limits, totalsDelta, limitsDelta, summary, false, write); err != nil {
			return after, false, err
		}
	}
	if len(events) == 0 {
		return after, true, nil
	}
	return events[len(events)-1].id, len(events) < repriceRawChunk, nil
}

func applyPriceEventTx(ctx context.Context, tx *sql.Tx, event pricingEvent, price pricing.Price,
	limits map[string][]currentCostLimit, totalsDelta map[pricingScope]int64, limitsDelta map[int64]int64,
	summary *application.ModelRepriceSummary, strict, write bool) (int64, error) {
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
	if write {
		if _, err := tx.ExecContext(ctx, `UPDATE usage_events SET cost_microdollars=? WHERE request_id=?`, newCost, event.id); err != nil {
			return 0, err
		}
	}
	if event.reservation.Valid && write {
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

// applyPriceDeltasTx validates aggregate deltas and, when write is set, applies them.
func applyPriceDeltasTx(ctx context.Context, tx *sql.Tx, totals map[pricingScope]int64, limits map[int64]int64, write bool) error {
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
		if !write {
			continue
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
		if !write {
			continue
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
