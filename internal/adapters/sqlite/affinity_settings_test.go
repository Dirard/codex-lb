package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"path/filepath"
	"testing"
)

func TestAffinitySettingsDefaultsCASAndRestart(t *testing.T) {
	ctx := context.Background()
	t.Setenv("CODEX_LB_OPENAI_CACHE_AFFINITY_MAX_AGE_SECONDS", "2400")
	store, path := testStore(t)
	settings, err := store.LoadSettings(ctx)
	if err != nil || settings.OpenAICacheAffinityMaxAgeSeconds != 2400 || settings.StickyReallocationPrimaryBudgetThresholdPct != 95 || settings.StickyReallocationSecondaryBudgetThresholdPct != 100 {
		t.Fatalf("affinity defaults: %+v %v", settings, err)
	}
	settings.OpenAICacheAffinityMaxAgeSeconds = 600
	settings.StickyReallocationPrimaryBudgetThresholdPct = 80
	settings.StickyReallocationSecondaryBudgetThresholdPct = 90
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSettings(ctx, settings); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale affinity settings version accepted: %v", err)
	}
	t.Setenv("CODEX_LB_OPENAI_CACHE_AFFINITY_MAX_AGE_SECONDS", "3600")
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	settings, err = reopened.LoadSettings(ctx)
	if err != nil || settings.OpenAICacheAffinityMaxAgeSeconds != 600 || settings.StickyReallocationPrimaryBudgetThresholdPct != 80 || settings.StickyReallocationSecondaryBudgetThresholdPct != 90 {
		t.Fatalf("restart overwrote affinity settings: %+v %v", settings, err)
	}
	for _, value := range []float64{-1, 101, math.NaN(), math.Inf(1)} {
		invalid := settings
		invalid.StickyReallocationPrimaryBudgetThresholdPct = value
		if err := reopened.SaveSettings(ctx, invalid); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid affinity pressure accepted: %v", err)
		}
	}
	settings.OpenAICacheAffinityMaxAgeSeconds = 0
	if err := reopened.SaveSettings(ctx, settings); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero affinity TTL accepted: %v", err)
	}
	t.Setenv("CODEX_LB_OPENAI_CACHE_AFFINITY_MAX_AGE_SECONDS", "invalid")
	if _, err := Open(filepath.Join(t.TempDir(), "bad.sqlite")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid startup affinity TTL accepted: %v", err)
	}
}

func TestAffinityLegacySettingsUseCanonicalPrimaryBeforeAlias(t *testing.T) {
	ctx := context.Background()
	for _, canonical := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy alias", true: "canonical wins"}[canonical], func(t *testing.T) {
			source, vault, _ := legacyFixture(t)
			legacy, err := sql.Open("sqlite", source)
			if err != nil {
				t.Fatal(err)
			}
			defer legacy.Close()
			if _, err := legacy.ExecContext(ctx, `ALTER TABLE dashboard_settings ADD COLUMN openai_cache_affinity_max_age_seconds INTEGER;
ALTER TABLE dashboard_settings ADD COLUMN sticky_reallocation_budget_threshold_pct REAL;
ALTER TABLE dashboard_settings ADD COLUMN sticky_reallocation_primary_budget_threshold_pct REAL;
ALTER TABLE dashboard_settings ADD COLUMN sticky_reallocation_secondary_budget_threshold_pct REAL;
UPDATE dashboard_settings SET openai_cache_affinity_max_age_seconds=120,
 sticky_reallocation_budget_threshold_pct=70,sticky_reallocation_secondary_budget_threshold_pct=85 WHERE id=1`); err != nil {
				t.Fatal(err)
			}
			want := 70.0
			if canonical {
				want = 60
				if _, err := legacy.ExecContext(ctx, "UPDATE dashboard_settings SET sticky_reallocation_primary_budget_threshold_pct=60 WHERE id=1"); err != nil {
					t.Fatal(err)
				}
			}
			if err := legacy.Close(); err != nil {
				t.Fatal(err)
			}
			store, _ := testStore(t)
			if _, err := store.ImportLegacySnapshot(ctx, source, vault); err != nil {
				t.Fatal(err)
			}
			settings, err := store.LoadSettings(ctx)
			if err != nil || settings.OpenAICacheAffinityMaxAgeSeconds != 120 || settings.StickyReallocationPrimaryBudgetThresholdPct != want || settings.StickyReallocationSecondaryBudgetThresholdPct != 85 {
				t.Fatalf("affinity import changed configuration: %+v %v", settings, err)
			}
		})
	}
}
