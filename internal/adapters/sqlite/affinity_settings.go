package sqlite

import (
	"context"
	"database/sql"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"codex-lb/internal/domain"
)

func validateAffinitySettings(settings domain.RuntimeSettings) error {
	if settings.OpenAICacheAffinityMaxAgeSeconds <= 0 || int64(settings.OpenAICacheAffinityMaxAgeSeconds) > math.MaxInt64/int64(time.Second) {
		return ErrInvalid
	}
	for _, value := range []float64{settings.StickyReallocationPrimaryBudgetThresholdPct, settings.StickyReallocationSecondaryBudgetThresholdPct} {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 100 {
			return ErrInvalid
		}
	}
	return nil
}

func loadAffinityEnvironment() (int, error) {
	value := 1800
	if raw, exists := os.LookupEnv("CODEX_LB_OPENAI_CACHE_AFFINITY_MAX_AGE_SECONDS"); exists {
		parsed, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			return 0, ErrInvalid
		}
		value = parsed
	}
	return value, validateAffinitySettings(domain.RuntimeSettings{OpenAICacheAffinityMaxAgeSeconds: value})
}

func importLegacyAffinitySettings(ctx context.Context, src, dst *sql.Tx) error {
	values := map[string]sql.NullFloat64{}
	for _, name := range []string{"openai_cache_affinity_max_age_seconds", "sticky_reallocation_budget_threshold_pct",
		"sticky_reallocation_primary_budget_threshold_pct", "sticky_reallocation_secondary_budget_threshold_pct"} {
		var present int
		if err := src.QueryRowContext(ctx, "SELECT count(*) FROM pragma_table_info('dashboard_settings') WHERE name=?", name).Scan(&present); err != nil {
			return err
		}
		if present == 0 {
			continue
		}
		var value sql.NullFloat64
		if err := src.QueryRowContext(ctx, "SELECT "+name+" FROM dashboard_settings WHERE id=1").Scan(&value); err != nil {
			return err
		}
		values[name] = value
	}
	settings := domain.RuntimeSettings{OpenAICacheAffinityMaxAgeSeconds: 1800,
		StickyReallocationPrimaryBudgetThresholdPct: 95, StickyReallocationSecondaryBudgetThresholdPct: 100}
	if value := values["openai_cache_affinity_max_age_seconds"]; value.Valid {
		if math.IsNaN(value.Float64) || math.IsInf(value.Float64, 0) || value.Float64 < 1 || value.Float64 > float64(math.MaxInt64/int64(time.Second)) || value.Float64 != math.Trunc(value.Float64) {
			return ErrInvalid
		}
		settings.OpenAICacheAffinityMaxAgeSeconds = int(value.Float64)
	}
	primary := values["sticky_reallocation_primary_budget_threshold_pct"]
	if !primary.Valid {
		primary = values["sticky_reallocation_budget_threshold_pct"]
	}
	if primary.Valid {
		settings.StickyReallocationPrimaryBudgetThresholdPct = primary.Float64
	}
	if secondary := values["sticky_reallocation_secondary_budget_threshold_pct"]; secondary.Valid {
		settings.StickyReallocationSecondaryBudgetThresholdPct = secondary.Float64
	}
	if err := validateAffinitySettings(settings); err != nil {
		return err
	}
	_, err := dst.ExecContext(ctx, `UPDATE runtime_settings SET openai_cache_affinity_max_age_seconds=?,
 sticky_reallocation_primary_budget_threshold_pct=?,sticky_reallocation_secondary_budget_threshold_pct=? WHERE id=1`,
		settings.OpenAICacheAffinityMaxAgeSeconds, settings.StickyReallocationPrimaryBudgetThresholdPct, settings.StickyReallocationSecondaryBudgetThresholdPct)
	return err
}
