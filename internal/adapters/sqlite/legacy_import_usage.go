package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strconv"
	"time"
)

func importLegacyQuota(ctx context.Context, src, dst *sql.Tx) error {
	rows, err := src.QueryContext(ctx, `SELECT account_id,coalesce(window,'primary'),used_percent,
 reset_at,window_minutes,recorded_at FROM (
 SELECT *,row_number() OVER (PARTITION BY account_id,coalesce(window,'primary')
 ORDER BY recorded_at DESC,id DESC) AS rank FROM usage_history
 ) WHERE rank=1 ORDER BY account_id,window`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var accountID, window string
		var used float64
		var reset, minutes sql.NullInt64
		var recorded any
		if err := rows.Scan(&accountID, &window, &used, &reset, &minutes, &recorded); err != nil {
			return err
		}
		if math.IsNaN(used) || math.IsInf(used, 0) || used < 0 {
			return fmt.Errorf("invalid legacy quota: %w", ErrInvalid)
		}
		at, err := parseLegacyTime(recorded)
		if err != nil {
			return err
		}
		var resetMs any
		if reset.Valid {
			resetMs = reset.Int64 * 1000
		}
		_, err = dst.ExecContext(ctx, `INSERT INTO account_quotas
 (account_id,window,used_percent,reset_at,window_minutes,observed_at)
 VALUES(?,?,?,?,?,?)`, accountID, window, used, resetMs, nullableInt(minutes), millis(at))
		if err != nil {
			return err
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	rows, err = src.QueryContext(ctx, `SELECT id,account_id,coalesce(window,'primary'),
 used_percent,reset_at,window_minutes,recorded_at FROM usage_history ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var legacyID int64
		var accountID, window string
		var used float64
		var reset, minutes sql.NullInt64
		var recorded any
		if err := rows.Scan(&legacyID, &accountID, &window, &used, &reset, &minutes, &recorded); err != nil {
			return err
		}
		if math.IsNaN(used) || math.IsInf(used, 0) || used < 0 {
			return fmt.Errorf("invalid legacy quota history: %w", ErrInvalid)
		}
		at, err := parseLegacyTime(recorded)
		if err != nil {
			return err
		}
		var resetMs any
		if reset.Valid {
			resetMs = reset.Int64 * 1000
		}
		_, err = dst.ExecContext(ctx, `INSERT INTO account_quota_history
 (legacy_id,account_id,window,observed_at,used_percent,reset_at,window_minutes)
 VALUES(?,?,?,?,?,?,?)`, legacyID, accountID, window, millis(at), used, resetMs, nullableInt(minutes))
		if err != nil {
			return err
		}
	}
	return rows.Err()
}

func importLegacyUsage(ctx context.Context, src, dst *sql.Tx, foldedRaw, hourlyRaw string, hourlyTime time.Time) error {
	if err := copyLegacyHourly(ctx, src, dst); err != nil {
		return err
	}
	if err := copyLegacyRequestLogs(ctx, src, dst); err != nil {
		return err
	}
	return importLegacyTotals(ctx, src, dst, foldedRaw, hourlyRaw, hourlyTime)
}

func copyLegacyHourly(ctx context.Context, src, dst *sql.Tx) error {
	rows, err := src.QueryContext(ctx, `SELECT bucket_epoch,account_id,api_key_id,model,
 service_tier,request_kind,is_deleted,request_count,error_count,cancelled_count,
 input_tokens,output_tokens,reasoning_tokens,output_or_reasoning_tokens,
 cached_input_tokens,cached_input_tokens_clamped,cost_usd,cost_count
 FROM request_usage_hourly_rollups ORDER BY bucket_epoch`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var bucket, requests, errorsCount, cancelled, input, output, reasoning, outputOrReasoning, cached, clamped, costCount int64
		var accountID, keyID, model, tier, kind string
		var deleted bool
		var cost float64
		if err := rows.Scan(&bucket, &accountID, &keyID, &model, &tier, &kind, &deleted, &requests,
			&errorsCount, &cancelled, &input, &output, &reasoning, &outputOrReasoning,
			&cached, &clamped, &cost, &costCount); err != nil {
			return err
		}
		if math.IsNaN(cost) || math.IsInf(cost, 0) {
			return fmt.Errorf("invalid historical cost: %w", ErrInvalid)
		}
		_, err = dst.ExecContext(ctx, `INSERT INTO legacy_hourly_usage
 (bucket_epoch,account_id,api_key_id,model,service_tier,request_kind,is_deleted,
 request_count,error_count,cancelled_count,input_tokens,output_tokens,reasoning_tokens,
 output_or_reasoning_tokens,cached_input_tokens,cached_input_tokens_clamped,cost_usd,cost_count)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, bucket, accountID, keyID, model, tier, kind,
			boolInt(deleted), requests, errorsCount, cancelled, input, output, reasoning,
			outputOrReasoning, cached, clamped, cost, costCount)
		if err != nil {
			return err
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	return copyLegacyHourlySatellites(ctx, src, dst)
}

func copyLegacyHourlySatellites(ctx context.Context, src, dst *sql.Tx) error {
	rows, err := src.QueryContext(ctx, `SELECT bucket_epoch,account_id,error_code,error_count
 FROM request_usage_hourly_error_rollups ORDER BY bucket_epoch`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var bucket, count int64
		var accountID, code string
		if err := rows.Scan(&bucket, &accountID, &code, &count); err != nil {
			rows.Close()
			return err
		}
		if _, err := dst.ExecContext(ctx, `INSERT INTO legacy_hourly_errors
 (bucket_epoch,account_id,error_code,error_count) VALUES(?,?,?,?)`, bucket, accountID, code, count); err != nil {
			rows.Close()
			return err
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	rows, err = src.QueryContext(ctx, `SELECT bucket_epoch,conversation_id,account_id,
 is_deleted,request_count FROM request_conversation_hourly_rollups ORDER BY bucket_epoch`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var bucket, count int64
		var conversationID, accountID string
		var deleted bool
		if err := rows.Scan(&bucket, &conversationID, &accountID, &deleted, &count); err != nil {
			return err
		}
		if _, err := dst.ExecContext(ctx, `INSERT INTO legacy_conversation_hourly
 (bucket_epoch,conversation_id,account_id,is_deleted,request_count)
 VALUES(?,?,?,?,?)`, bucket, conversationID, accountID, boolInt(deleted), count); err != nil {
			return err
		}
	}
	return rows.Err()
}

func copyLegacyRequestLogs(ctx context.Context, src, dst *sql.Tx) error {
	rows, err := src.QueryContext(ctx, `SELECT id,account_id,api_key_id,model_source_id,
 request_id,request_kind,requested_at,model,service_tier,input_tokens,
 output_tokens,cached_input_tokens,reasoning_tokens,cost_usd,status,error_code,
 latency_queue_ms,latency_first_upstream_event_ms,latency_ms,deleted_at,
 conversation_id,useragent,useragent_group,client_ip,reasoning_effort,plan_type,
 source,transport,latency_first_token_ms
 FROM request_logs ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var account, key, sourceID, tier, errorCode sql.NullString
		var conversation, useragent, useragentGroup, clientIP, effort, planType, sourceName, transport sql.NullString
		var sourceRequestID, kind, model, status string
		var input, output, cached, reasoning, queue, first, total, firstToken sql.NullInt64
		var cost sql.NullFloat64
		var requested, deleted any
		if err := rows.Scan(&id, &account, &key, &sourceID, &sourceRequestID, &kind, &requested,
			&model, &tier, &input, &output, &cached, &reasoning, &cost, &status, &errorCode,
			&queue, &first, &total, &deleted, &conversation, &useragent, &useragentGroup,
			&clientIP, &effort, &planType, &sourceName, &transport, &firstToken); err != nil {
			return err
		}
		requestedAt, err := parseLegacyTime(requested)
		if err != nil {
			return err
		}
		costMicro, err := legacyCostMicro(cost)
		if err != nil {
			return err
		}
		outputAmount := int64(0)
		if output.Valid {
			outputAmount = output.Int64
		} else if reasoning.Valid {
			outputAmount = reasoning.Int64
		}
		_, err = dst.ExecContext(ctx, `INSERT INTO usage_events
 (request_id,reservation_id,api_key_id,account_id,model_source_id,model,
 service_tier,request_kind,status,error_code,requested_at,queue_latency_ms,
 first_event_ms,total_latency_ms,input_tokens,output_tokens,cached_input_tokens,
 reasoning_tokens,cost_microdollars,legacy_request_id,legacy_deleted,
 conversation_id,useragent,useragent_group,client_ip,reasoning_effort,plan_type,
 source,transport,latency_first_token_ms,reasoning_tokens_known)
 VALUES(?,NULL,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			"legacy-log:"+strconv.FormatInt(id, 10), nullText(key), nullText(account), nullText(sourceID),
			model, textVal(tier), kind, status, textVal(errorCode), millis(requestedAt),
			zeroIfNull(queue), zeroIfNull(first), zeroIfNull(total),
			zeroIfNull(input), outputAmount, zeroIfNull(cached), zeroIfNull(reasoning),
			costMicro, sourceRequestID, boolInt(deleted != nil), conversationValue(textVal(conversation)),
			metadataValue(textVal(useragent), 1024), metadataValue(textVal(useragentGroup), 128),
			metadataValue(textVal(clientIP), 128), metadataValue(textVal(effort), 32),
			metadataValue(textVal(planType), 64), metadataValue(textVal(sourceName), 128),
			metadataValue(textVal(transport), 32), nullableInt(firstToken), boolInt(reasoning.Valid))
		if err != nil {
			return fmt.Errorf("import legacy request log %d: %w", id, err)
		}
	}
	return rows.Err()
}

func zeroIfNull(v sql.NullInt64) int64 {
	if !v.Valid {
		return 0
	}
	return v.Int64
}

func legacyCostMicro(v sql.NullFloat64) (int64, error) {
	if !v.Valid {
		return 0, nil
	}
	if math.IsNaN(v.Float64) || math.IsInf(v.Float64, 0) || v.Float64 < 0 || v.Float64 > float64(math.MaxInt64)/1e6 {
		return 0, fmt.Errorf("invalid legacy cost: %w", ErrInvalid)
	}
	return int64(math.Round(v.Float64 * 1e6)), nil
}
