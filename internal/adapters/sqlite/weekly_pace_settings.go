package sqlite

import (
	"context"
	"database/sql"
	"slices"
	"strconv"
	"strings"

	"codex-lb/internal/domain"
)

const schemaV28 = `
ALTER TABLE runtime_settings ADD COLUMN weekly_pace_working_days TEXT NOT NULL DEFAULT '0,1,2,3,4,5,6';
ALTER TABLE runtime_settings ADD COLUMN weekly_pace_smoothing_minutes INTEGER NOT NULL DEFAULT 30;
`

func normalizeWeeklyPaceSettings(settings *domain.RuntimeSettings) error {
	if len(settings.WeeklyPaceWorkingDays) == 0 || !slices.Contains([]int{15, 30, 60, 120, 240}, settings.WeeklyPaceSmoothingMinutes) {
		return ErrInvalid
	}
	days := []int{}
	for _, raw := range strings.Split(settings.WeeklyPaceWorkingDays, ",") {
		day, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || day < 0 || day > 6 {
			return ErrInvalid
		}
		days = append(days, day)
	}
	slices.Sort(days)
	parts := []string{}
	for _, day := range slices.Compact(days) {
		parts = append(parts, strconv.Itoa(day))
	}
	settings.WeeklyPaceWorkingDays = strings.Join(parts, ",")
	return nil
}

func importLegacyWeeklyPaceSettings(ctx context.Context, src, dst *sql.Tx) error {
	settings := domain.RuntimeSettings{WeeklyPaceWorkingDays: "0,1,2,3,4,5,6", WeeklyPaceSmoothingMinutes: 30}
	for _, name := range []string{"weekly_pace_working_days", "weekly_pace_smoothing_minutes"} {
		var present int
		if err := src.QueryRowContext(ctx, "SELECT count(*) FROM pragma_table_info('dashboard_settings') WHERE name=?", name).Scan(&present); err != nil {
			return err
		}
		if present == 0 {
			continue
		}
		if name == "weekly_pace_working_days" {
			var value sql.NullString
			if err := src.QueryRowContext(ctx, "SELECT weekly_pace_working_days FROM dashboard_settings WHERE id=1").Scan(&value); err != nil {
				return err
			}
			if value.Valid {
				settings.WeeklyPaceWorkingDays = value.String
			}
		} else {
			var value sql.NullInt64
			if err := src.QueryRowContext(ctx, "SELECT weekly_pace_smoothing_minutes FROM dashboard_settings WHERE id=1").Scan(&value); err != nil {
				return err
			}
			if value.Valid {
				if value.Int64 < 0 || value.Int64 > 240 {
					return ErrInvalid
				}
				settings.WeeklyPaceSmoothingMinutes = int(value.Int64)
			}
		}
	}
	if err := normalizeWeeklyPaceSettings(&settings); err != nil {
		return err
	}
	_, err := dst.ExecContext(ctx, "UPDATE runtime_settings SET weekly_pace_working_days=?,weekly_pace_smoothing_minutes=? WHERE id=1", settings.WeeklyPaceWorkingDays, settings.WeeklyPaceSmoothingMinutes)
	return err
}
