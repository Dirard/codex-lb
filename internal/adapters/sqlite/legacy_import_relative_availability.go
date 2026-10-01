package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"math"
)

func importLegacyRelativeAvailabilitySettings(ctx context.Context, src, dst *sql.Tx) error {
	var columns int
	if err := src.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('dashboard_settings')
 WHERE name IN ('relative_availability_power','relative_availability_top_k')`).Scan(&columns); err != nil {
		return err
	}
	if columns == 0 { // Older snapshots inherit the Go defaults.
		return nil
	}
	if columns != 2 {
		return fmt.Errorf("partial legacy relative availability settings: %w", ErrInvalid)
	}
	var power float64
	var topK int
	if err := src.QueryRowContext(ctx, `SELECT relative_availability_power,relative_availability_top_k
 FROM dashboard_settings WHERE id=1`).Scan(&power, &topK); err != nil {
		return err
	}
	if !(power > 0) || math.IsNaN(power) || math.IsInf(power, 0) || topK < 1 || topK > 20 {
		return fmt.Errorf("invalid legacy relative availability settings: %w", ErrInvalid)
	}
	_, err := dst.ExecContext(ctx, `UPDATE runtime_settings SET relative_availability_power=?,relative_availability_top_k=? WHERE id=1`, power, topK)
	return err
}
