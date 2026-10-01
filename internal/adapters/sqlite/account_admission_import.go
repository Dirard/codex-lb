package sqlite

import (
	"context"
	"database/sql"
)

func importLegacyAccountAdmissionSettings(ctx context.Context, src, dst *sql.Tx) error {
	names := [4]string{
		"proxy_account_response_create_limit", "proxy_account_stream_limit",
		"proxy_account_stream_recovery_reserve", "proxy_api_key_fair_share_congestion_threshold_pct",
	}
	var values [4]sql.NullInt64
	for i, name := range names {
		var present int
		if err := src.QueryRowContext(ctx, "SELECT count(*) FROM pragma_table_info('dashboard_settings') WHERE name=?", name).Scan(&present); err != nil {
			return err
		}
		if present == 0 {
			continue
		}
		if err := src.QueryRowContext(ctx, "SELECT "+name+" FROM dashboard_settings WHERE id=1").Scan(&values[i]); err != nil {
			return err
		}
		if values[i].Valid && (values[i].Int64 < 0 || i == 3 && values[i].Int64 > 100) {
			return ErrInvalid
		}
	}
	if values[1].Valid && values[2].Valid && values[1].Int64 > 0 && values[2].Int64 > values[1].Int64 {
		return ErrInvalid
	}
	_, err := dst.ExecContext(ctx, `UPDATE runtime_settings SET
 proxy_account_response_create_limit=?,proxy_account_stream_limit=?,
 proxy_account_stream_recovery_reserve=?,proxy_api_key_fair_share_congestion_threshold_pct=? WHERE id=1`,
		nullableInt(values[0]), nullableInt(values[1]), nullableInt(values[2]), nullableInt(values[3]))
	return err
}
