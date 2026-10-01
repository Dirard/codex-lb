package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"
)

func TestSettingsRetentionAndEnumValidation(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()
	settings, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	days := 30
	settings.RequestLogRetentionDays = &days
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	settings, err = store.LoadSettings(ctx)
	if err != nil || settings.RequestLogRetentionDays == nil || *settings.RequestLogRetentionDays != 30 {
		t.Fatal("retention setting not preserved")
	}
	for _, days := range []int{-1, 1, 29, 3651} {
		settings.RequestLogRetentionDays = &days
		if err := store.SaveSettings(ctx, settings); err == nil {
			t.Fatal("unsafe retention accepted")
		}
	}
	settings.RequestLogRetentionDays = nil
	settings.RoutingStrategy = "typo"
	if err := store.SaveSettings(ctx, settings); err == nil {
		t.Fatal("unknown routing strategy accepted")
	}
	settings.RoutingStrategy = "capacity_weighted"
	historyDays := 45
	settings.UsageHistoryRetentionDays = &historyDays
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	settings, err = store.LoadSettings(ctx)
	if err != nil || settings.RequestLogRetentionDays != nil || settings.UsageHistoryRetentionDays == nil ||
		*settings.UsageHistoryRetentionDays != 45 {
		t.Fatal("nullable retention reset not applied")
	}
	for _, days := range []int{-1, 1, 44, 3651} {
		settings.UsageHistoryRetentionDays = &days
		if err := store.SaveSettings(ctx, settings); err == nil {
			t.Fatalf("unsafe usage history retention %d accepted", days)
		}
	}
	historyDays = 0
	settings.UsageHistoryRetentionDays = &historyDays
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	settings, err = store.LoadSettings(ctx)
	if err != nil || settings.UsageHistoryRetentionDays == nil || *settings.UsageHistoryRetentionDays != 0 {
		t.Fatal("disabled usage history retention not preserved")
	}
}

func TestRelativeAvailabilitySettingsPersistAndRejectInvalidValues(t *testing.T) {
	ctx := context.Background()
	store, path := testStore(t)
	settings, err := store.LoadSettings(ctx)
	if err != nil || settings.RelativeAvailabilityPower != 2 || settings.RelativeAvailabilityTopK != 5 {
		t.Fatalf("relative availability defaults = %+v %v", settings, err)
	}
	settings.RelativeAvailabilityPower = 1.35
	settings.RelativeAvailabilityTopK = 7
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	stored, err := reopened.LoadSettings(ctx)
	if err != nil || stored.RelativeAvailabilityPower != 1.35 || stored.RelativeAvailabilityTopK != 7 {
		t.Fatalf("relative availability lost after restart = %+v %v", stored, err)
	}
	for _, power := range []float64{0, -1, math.NaN(), math.Inf(1), math.Inf(-1)} {
		invalid := stored
		invalid.RelativeAvailabilityPower = power
		if err := reopened.SaveSettings(ctx, invalid); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid power %v accepted: %v", power, err)
		}
	}
	for _, topK := range []int{0, -1, 21} {
		invalid := stored
		invalid.RelativeAvailabilityTopK = topK
		if err := reopened.SaveSettings(ctx, invalid); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid top K %d accepted: %v", topK, err)
		}
	}
	unchanged, err := reopened.LoadSettings(ctx)
	if err != nil || unchanged.RelativeAvailabilityPower != 1.35 || unchanged.RelativeAvailabilityTopK != 7 {
		t.Fatalf("rejected values changed stored settings: %+v %v", unchanged, err)
	}
}

func TestRelativeAvailabilityV23UpgradeKeepsExistingSettings(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v23.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:23] {
		if _, err := db.ExecContext(ctx, migration); err != nil {
			t.Fatal(err)
		}
	}
	for _, statement := range []string{
		"UPDATE runtime_settings SET routing_strategy='relative_availability' WHERE id=1",
		fmt.Sprintf("PRAGMA application_id=%d", applicationID),
		"PRAGMA user_version=23",
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	settings, err := store.LoadSettings(ctx)
	if err != nil || settings.RoutingStrategy != "relative_availability" || settings.RelativeAvailabilityPower != 2 || settings.RelativeAvailabilityTopK != 5 {
		t.Fatalf("V23 upgrade changed routing settings: %+v %v", settings, err)
	}
}

func TestResetCreditSettingsV24UpgradeAndPersistence(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v24.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:24] {
		if _, err := db.ExecContext(ctx, migration); err != nil {
			t.Fatal(err)
		}
	}
	for _, statement := range []string{
		"UPDATE runtime_settings SET prohibit_fast_mode=1 WHERE id=1",
		fmt.Sprintf("PRAGMA application_id=%d", applicationID),
		"PRAGMA user_version=24",
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := store.LoadSettings(ctx)
	if err != nil || !settings.ShowResetCreditBadges || !settings.ShowResetCreditExpiryBadge || !settings.ProhibitFastMode {
		t.Fatalf("V24 upgrade settings = %+v %v", settings, err)
	}
	settings.ShowResetCreditBadges = false
	settings.ShowResetCreditExpiryBadge = false
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	stored, err := store.LoadSettings(ctx)
	if err != nil || stored.ShowResetCreditBadges || stored.ShowResetCreditExpiryBadge || !stored.ProhibitFastMode {
		t.Fatalf("reset-credit settings lost after restart = %+v %v", stored, err)
	}
}

func TestWarmupModelV25UpgradeAndPersistence(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v25.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:25] {
		if _, err := db.ExecContext(ctx, migration); err != nil {
			t.Fatal(err)
		}
	}
	for _, statement := range []string{
		"UPDATE runtime_settings SET show_reset_credit_badges=0 WHERE id=1",
		fmt.Sprintf("PRAGMA application_id=%d", applicationID),
		"PRAGMA user_version=25",
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := store.LoadSettings(ctx)
	if err != nil || settings.WarmupModel != "gpt-5.4-mini" || settings.ShowResetCreditBadges {
		t.Fatalf("V25 upgrade settings = %+v %v", settings, err)
	}
	settings.WarmupModel = " gpt-6-sol "
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	stored, err := store.LoadSettings(ctx)
	if err != nil || stored.WarmupModel != "gpt-6-sol" || stored.ShowResetCreditBadges {
		t.Fatalf("warmup model lost after restart = %+v %v", stored, err)
	}
	for _, invalid := range []string{"  ", strings.Repeat("x", 129)} {
		stored.WarmupModel = invalid
		if err := store.SaveSettings(ctx, stored); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid warmup model %q accepted: %v", invalid, err)
		}
	}
}
