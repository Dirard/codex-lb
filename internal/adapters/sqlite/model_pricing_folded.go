package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"math"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
	"codex-lb/internal/domain/pricing"
)

type foldedPriceBucket struct {
	epoch, requests, input, output, cached int64
	account, key, model, tier, kind        string
	accountKind                            domain.AccountKind
	deleted                                bool
	costUSD                                float64
}

func repriceFoldedPriceHistoryTx(ctx context.Context, tx *sql.Tx, affected []string,
	limits map[string][]currentCostLimit, totals map[pricingScope]int64, limitDeltas map[int64]int64,
	summary *application.ModelRepriceSummary) error {
	for _, model := range affected {
		price, priced, err := resolveCodexPriceTx(ctx, tx, model)
		if err != nil || !priced {
			return err
		}
		for offset := 0; ; offset += 500 {
			rows, err := tx.QueryContext(ctx, `SELECT h.bucket_epoch,h.account_id,h.api_key_id,h.model,
 h.service_tier,h.request_kind,h.is_deleted,h.request_count,h.input_tokens,
 h.output_or_reasoning_tokens,h.cached_input_tokens,h.cost_usd,coalesce(a.kind,'')
 FROM legacy_hourly_usage h LEFT JOIN accounts a ON a.id=CASE
 WHEN h.account_id=char(31) THEN NULL
 WHEN substr(h.account_id,1,1)=char(31) THEN substr(h.account_id,2)
 ELSE h.account_id END WHERE h.model=? ORDER BY h.bucket_epoch,h.account_id,h.api_key_id,
 h.service_tier,h.request_kind,h.is_deleted LIMIT 500 OFFSET ?`, model, offset)
			if err != nil {
				return err
			}
			buckets := make([]foldedPriceBucket, 0, 500)
			for rows.Next() {
				var bucket foldedPriceBucket
				if err := rows.Scan(&bucket.epoch, &bucket.account, &bucket.key, &bucket.model,
					&bucket.tier, &bucket.kind, &bucket.deleted, &bucket.requests, &bucket.input,
					&bucket.output, &bucket.cached, &bucket.costUSD, &bucket.accountKind); err != nil {
					rows.Close()
					return err
				}
				buckets = append(buckets, bucket)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			if len(buckets) == 0 {
				break
			}
			for _, bucket := range buckets {
				if bucket.accountKind == domain.AccountExternal {
					continue
				}
				covered := false
				if bucket.accountKind == domain.AccountChatGPT && bucket.kind != "transcription" && bucket.kind != "reconciled" {
					covered, err = repriceCoveredBucketTx(ctx, tx, bucket, price, limits, totals, limitDeltas, summary)
					if err != nil {
						return err
					}
				}
				if !covered {
					summary.PartialFoldedBuckets++
					summary.PartialFoldedRequests, err = addPriceDelta(summary.PartialFoldedRequests, bucket.requests)
					if err != nil {
						return err
					}
				}
			}
			if len(buckets) < 500 {
				break
			}
		}
	}
	return nil
}

func foldedRawWhere(bucket foldedPriceBucket) (string, []any) {
	var account, key any
	if bucket.account != "\x1f" {
		account = fromLegacyDimension(bucket.account)
	}
	if bucket.key != "\x1f" {
		key = fromLegacyDimension(bucket.key)
	}
	tier := bucket.tier
	if tier == "\x1f" {
		tier = ""
	}
	where := `e.model=? AND e.request_kind=? AND e.legacy_deleted=?
 AND e.account_id IS ? AND e.api_key_id IS ? AND e.service_tier=?
 AND e.legacy_request_id IS NOT NULL AND e.model_source_id IS NULL
 AND e.requested_at>=? AND e.requested_at<? AND e.requested_at<coalesce(
 (SELECT hourly_folded_through FROM legacy_import_state WHERE id=1),-9223372036854775808)`
	return where, []any{bucket.model, bucket.kind, boolInt(bucket.deleted), account, key,
		tier, bucket.epoch * 1000, (bucket.epoch + 3600) * 1000}
}

func repriceCoveredBucketTx(ctx context.Context, tx *sql.Tx, bucket foldedPriceBucket, price pricing.Price,
	limits map[string][]currentCostLimit, totals map[pricingScope]int64, limitDeltas map[int64]int64,
	summary *application.ModelRepriceSummary) (bool, error) {
	where, args := foldedRawWhere(bucket)
	var count, input, output, cached, oldCost, invalid int64
	err := tx.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(e.input_tokens),0),
 coalesce(sum(e.output_tokens),0),coalesce(sum(e.cached_input_tokens),0),
 coalesce(sum(e.cost_microdollars),0),coalesce(sum(e.input_tokens<0 OR e.output_tokens<0
 OR e.cached_input_tokens<0 OR e.cached_input_tokens>e.input_tokens OR
 (e.input_tokens=0 AND e.output_tokens=0 AND e.cost_microdollars>0)),0)
 FROM usage_events e WHERE `+where, args...).Scan(&count, &input, &output, &cached, &oldCost, &invalid)
	if err != nil {
		return false, err
	}
	if count != bucket.requests || input != bucket.input || output != bucket.output ||
		cached != bucket.cached || invalid != 0 ||
		math.Abs(bucket.costUSD-float64(oldCost)/1e6) > float64(count+1)*0.000001 {
		return false, nil
	}
	after := ""
	processed := int64(0)
	deltaTotal := int64(0)
	for {
		rows, err := tx.QueryContext(ctx, `SELECT e.request_id,e.model,e.service_tier,e.request_kind,
 e.account_id,e.api_key_id,e.reservation_id,e.legacy_request_id,e.requested_at,
 e.input_tokens,e.output_tokens,e.cached_input_tokens,e.cost_microdollars,e.legacy_deleted
 FROM usage_events e WHERE `+where+` AND e.request_id>? ORDER BY e.request_id LIMIT 500`,
			append(append([]any{}, args...), after, 500)...)
		if err != nil {
			return false, err
		}
		events := make([]pricingEvent, 0, 500)
		for rows.Next() {
			var event pricingEvent
			if err := rows.Scan(&event.id, &event.model, &event.tier, &event.kind,
				&event.account, &event.key, &event.reservation, &event.legacyID,
				&event.requested, &event.input, &event.output, &event.cached,
				&event.oldCost, &event.deleted); err != nil {
				rows.Close()
				return false, err
			}
			events = append(events, event)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return false, err
		}
		if len(events) == 0 {
			break
		}
		for _, event := range events {
			delta, err := applyPriceEventTx(ctx, tx, event, price, limits, totals, limitDeltas, summary, true)
			if err != nil {
				return false, err
			}
			deltaTotal, err = addPriceDelta(deltaTotal, delta)
			if err != nil {
				return false, err
			}
			processed++
		}
		after = events[len(events)-1].id
	}
	if processed != bucket.requests {
		return false, fmt.Errorf("covered rollup changed during repricing: %w", ErrConflict)
	}
	newUSD := bucket.costUSD + float64(deltaTotal)/1e6
	if newUSD < -0.000001 {
		return false, ErrInvalid
	}
	if _, err := tx.ExecContext(ctx, `UPDATE legacy_hourly_usage SET cost_usd=?
 WHERE bucket_epoch=? AND account_id=? AND api_key_id=? AND model=? AND service_tier=?
 AND request_kind=? AND is_deleted=?`, max(0, newUSD), bucket.epoch, bucket.account,
		bucket.key, bucket.model, bucket.tier, bucket.kind, boolInt(bucket.deleted)); err != nil {
		return false, err
	}
	return true, nil
}
