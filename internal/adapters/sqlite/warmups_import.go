package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
)

// importLegacyWarmupSettings preserves optional columns from older and current
// dashboard_settings schemas. Every queried name comes from this fixed list.
func importLegacyWarmupSettings(ctx context.Context, src, dst *sql.Tx) error {
	rows, err := src.QueryContext(ctx, "SELECT name FROM pragma_table_info('dashboard_settings')")
	if err != nil {
		return err
	}
	columns := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		columns[name] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, name := range []string{
		"warmup_model",
		"limit_warmup_windows", "limit_warmup_prompt", "limit_warmup_cooldown_seconds",
		"limit_warmup_exhausted_threshold_percent", "limit_warmup_idle_threshold_percent",
		"limit_warmup_min_available_percent", "limit_warmup_staggered_idle_enabled",
	} {
		if !columns[name] {
			continue
		}
		var value any
		if err := src.QueryRowContext(ctx, "SELECT "+name+" FROM dashboard_settings WHERE id=1").Scan(&value); err != nil {
			return err
		}
		if value == nil { // A nullable old column inherits the safe Go default.
			continue
		}
		if name == "warmup_model" {
			if model, ok := value.(string); ok {
				value = strings.TrimSpace(model)
			}
		}
		if !validLegacyWarmupSetting(name, value) {
			return fmt.Errorf("legacy %s: %w", name, ErrInvalid)
		}
		if _, err := dst.ExecContext(ctx, "UPDATE runtime_settings SET "+name+"=? WHERE id=1", value); err != nil {
			return err
		}
	}
	return nil
}

func validLegacyWarmupSetting(name string, value any) bool {
	switch name {
	case "warmup_model":
		model, ok := value.(string)
		return ok && model != "" && len(model) <= 128
	case "limit_warmup_windows":
		text, ok := value.(string)
		return ok && slices.Contains([]string{"primary", "secondary", "both"}, text)
	case "limit_warmup_prompt":
		text, ok := value.(string)
		return ok && text != "" && len(text) <= 512
	case "limit_warmup_cooldown_seconds":
		seconds, ok := value.(int64)
		return ok && seconds >= 60
	case "limit_warmup_staggered_idle_enabled":
		enabled, ok := value.(int64)
		return ok && (enabled == 0 || enabled == 1)
	default:
		percent, ok := value.(float64)
		if !ok {
			integer, isInteger := value.(int64)
			if !isInteger {
				return false
			}
			percent = float64(integer)
		}
		return validWarmupPercent(percent)
	}
}
