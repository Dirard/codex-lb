package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type legacySums struct {
	count, failed, input, output, cached, reasoning int64
	costUSD                                         float64
}

func importLegacyTotals(ctx context.Context, src, dst *sql.Tx, foldedRaw, hourlyRaw string, hourlyTime time.Time) error {
	for _, source := range []struct{ table, id, scope string }{
		{"account_usage_rollups", "account_id", "account"},
		{"api_key_usage_rollups", "api_key_id", "key"},
	} {
		rows, err := src.QueryContext(ctx, "SELECT "+source.id+`,request_count,input_tokens,
 output_tokens,cached_input_tokens,total_cost_usd FROM `+source.table)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			var sums legacySums
			if err := rows.Scan(&id, &sums.count, &sums.input, &sums.output, &sums.cached, &sums.costUSD); err != nil {
				rows.Close()
				return err
			}
			if err := addLegacyTotals(ctx, dst, source.scope, id, sums); err != nil {
				rows.Close()
				return err
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	accountTail := `SELECT l.account_id,count(*),coalesce(sum(l.input_tokens),0),
 coalesce(sum(coalesce(l.output_tokens,l.reasoning_tokens,0)),0),
 coalesce(sum(l.cached_input_tokens),0),coalesce(sum(l.cost_usd),0)
 FROM request_logs l JOIN (
 SELECT max(id) AS id FROM request_logs WHERE requested_at>? AND
 request_kind NOT IN ('warmup','limit_warmup') AND deleted_at IS NULL AND account_id IS NOT NULL
 GROUP BY account_id,request_id,requested_at
 ) d ON d.id=l.id GROUP BY l.account_id`
	if err := addLegacyTail(ctx, src, dst, "account", accountTail, foldedRaw); err != nil {
		return err
	}
	keyTail := `SELECT api_key_id,count(*),coalesce(sum(input_tokens),0),
 coalesce(sum(coalesce(output_tokens,reasoning_tokens,0)),0),
 coalesce(sum(cached_input_tokens),0),coalesce(sum(cost_usd),0)
 FROM request_logs WHERE requested_at>? AND request_kind NOT IN ('warmup','limit_warmup')
 AND api_key_id IS NOT NULL GROUP BY api_key_id`
	if err := addLegacyTail(ctx, src, dst, "key", keyTail, foldedRaw); err != nil {
		return err
	}
	var global legacySums
	if err := dst.QueryRowContext(ctx, `SELECT coalesce(sum(request_count),0),
 coalesce(sum(error_count+cancelled_count),0),coalesce(sum(input_tokens),0),
 coalesce(sum(output_or_reasoning_tokens),0),coalesce(sum(cached_input_tokens),0),
 coalesce(sum(reasoning_tokens),0),coalesce(sum(cost_usd),0)
 FROM legacy_hourly_usage WHERE bucket_epoch<?`, hourlyTime.Unix()).Scan(&global.count,
		&global.failed, &global.input, &global.output, &global.cached, &global.reasoning, &global.costUSD); err != nil {
		return err
	}
	if err := addLegacyTotals(ctx, dst, "all", "all", global); err != nil {
		return err
	}
	if err := src.QueryRowContext(ctx, `SELECT count(*),
 coalesce(sum(status NOT IN ('success','completed')),0),coalesce(sum(input_tokens),0),
 coalesce(sum(coalesce(output_tokens,reasoning_tokens,0)),0),
 coalesce(sum(cached_input_tokens),0),coalesce(sum(reasoning_tokens),0),
 coalesce(sum(cost_usd),0) FROM request_logs WHERE requested_at>=?`, hourlyRaw).
		Scan(&global.count, &global.failed, &global.input, &global.output, &global.cached,
			&global.reasoning, &global.costUSD); err != nil {
		return err
	}
	if err := addLegacyTotals(ctx, dst, "all", "all", global); err != nil {
		return err
	}
	if err := importLegacyAuxTotals(ctx, src, dst, hourlyRaw, hourlyTime); err != nil {
		return err
	}
	return nil
}

func addLegacyTail(ctx context.Context, src, dst *sql.Tx, scope, query, foldedRaw string) error {
	rows, err := src.QueryContext(ctx, query, foldedRaw)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var sums legacySums
		if err := rows.Scan(&id, &sums.count, &sums.input, &sums.output, &sums.cached, &sums.costUSD); err != nil {
			return err
		}
		if err := addLegacyTotals(ctx, dst, scope, id, sums); err != nil {
			return err
		}
	}
	return rows.Err()
}

func addLegacyTotals(ctx context.Context, tx *sql.Tx, scope, id string, v legacySums) error {
	cost, err := legacyCostMicro(sql.NullFloat64{Float64: v.costUSD, Valid: true})
	if err != nil {
		return err
	}
	if v.count < 0 || v.failed < 0 || v.input < 0 || v.output < 0 || v.cached < 0 || v.reasoning < 0 {
		return fmt.Errorf("negative legacy usage total: %w", ErrInvalid)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO usage_totals
 (scope,scope_id,request_count,failed_count,input_tokens,output_tokens,cached_input_tokens,
 reasoning_tokens,cost_microdollars) VALUES(?,?,?,?,?,?,?,?,?)
 ON CONFLICT(scope,scope_id) DO UPDATE SET
 request_count=request_count+excluded.request_count,failed_count=failed_count+excluded.failed_count,
 input_tokens=input_tokens+excluded.input_tokens,output_tokens=output_tokens+excluded.output_tokens,
 cached_input_tokens=cached_input_tokens+excluded.cached_input_tokens,
 reasoning_tokens=reasoning_tokens+excluded.reasoning_tokens,
 cost_microdollars=cost_microdollars+excluded.cost_microdollars`,
		scope, id, v.count, v.failed, v.input, v.output, v.cached, v.reasoning, cost)
	return err
}

func importLegacyAuxTotals(ctx context.Context, src, dst *sql.Tx, hourlyRaw string, hourlyTime time.Time) error {
	for _, scope := range []string{"account", "key"} {
		column, filter := "account_id", "is_deleted=0"
		if scope == "key" {
			column, filter = "api_key_id", "1=1"
		}
		query := "SELECT " + column + `,coalesce(sum(error_count+cancelled_count),0),
 coalesce(sum(reasoning_tokens),0) FROM legacy_hourly_usage WHERE bucket_epoch<?
 AND ` + column + `!=char(31) AND request_kind NOT IN ('warmup','limit_warmup') AND ` + filter + ` GROUP BY ` + column
		rows, err := dst.QueryContext(ctx, query, hourlyTime.Unix())
		if err != nil {
			return err
		}
		type aux struct {
			id                string
			failed, reasoning int64
		}
		var amounts []aux
		for rows.Next() {
			var a aux
			if err := rows.Scan(&a.id, &a.failed, &a.reasoning); err != nil {
				rows.Close()
				return err
			}
			a.id = fromLegacyDimension(a.id)
			amounts = append(amounts, a)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, a := range amounts {
			if err := addLegacyAux(ctx, dst, scope, a.id, a.failed, a.reasoning); err != nil {
				return err
			}
		}
		var tail string
		if scope == "account" {
			tail = `SELECT l.account_id,coalesce(sum(l.status NOT IN ('success','completed')),0),
 coalesce(sum(l.reasoning_tokens),0) FROM request_logs l JOIN (
 SELECT max(id) AS id FROM request_logs WHERE requested_at>=? AND
 request_kind NOT IN ('warmup','limit_warmup') AND deleted_at IS NULL AND account_id IS NOT NULL
 GROUP BY account_id,request_id,requested_at
 ) d ON d.id=l.id GROUP BY l.account_id`
		} else {
			tail = `SELECT api_key_id,coalesce(sum(status NOT IN ('success','completed')),0),
 coalesce(sum(reasoning_tokens),0) FROM request_logs WHERE requested_at>=?
 AND request_kind NOT IN ('warmup','limit_warmup') AND api_key_id IS NOT NULL GROUP BY api_key_id`
		}
		rows, err = src.QueryContext(ctx, tail, hourlyRaw)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			var failed, reasoning int64
			if err := rows.Scan(&id, &failed, &reasoning); err != nil {
				rows.Close()
				return err
			}
			if err := addLegacyAux(ctx, dst, scope, id, failed, reasoning); err != nil {
				rows.Close()
				return err
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func addLegacyAux(ctx context.Context, dst *sql.Tx, scope, id string, failed, reasoning int64) error {
	if failed < 0 || reasoning < 0 {
		return fmt.Errorf("negative legacy outcome total: %w", ErrInvalid)
	}
	_, err := dst.ExecContext(ctx, `INSERT INTO usage_totals
 (scope,scope_id,failed_count,reasoning_tokens) VALUES(?,?,?,?)
 ON CONFLICT(scope,scope_id) DO UPDATE SET
 failed_count=failed_count+excluded.failed_count,
 reasoning_tokens=reasoning_tokens+excluded.reasoning_tokens`, scope, id, failed, reasoning)
	return err
}

func fromLegacyDimension(v string) string {
	if strings.HasPrefix(v, "\x1f\x1f") {
		return v[1:]
	}
	return v
}
