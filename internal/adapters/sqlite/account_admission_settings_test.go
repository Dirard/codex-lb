package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

func TestAccountAdmissionSettingsEnvOverridesAndRestart(t *testing.T) {
	t.Setenv("CODEX_LB_PROXY_ACCOUNT_RESPONSE_CREATE_LIMIT", "4")
	t.Setenv("CODEX_LB_PROXY_ACCOUNT_STREAM_LIMIT", "8")
	t.Setenv("CODEX_LB_PROXY_ACCOUNT_STREAM_RECOVERY_RESERVE", "1")
	t.Setenv("CODEX_LB_PROXY_API_KEY_FAIR_SHARE_CONGESTION_THRESHOLD_PCT", "0")
	store, path := testStore(t)
	ctx := context.Background()
	settings, err := store.LoadSettings(ctx)
	if err != nil || settings.ProxyAccountResponseCreateLimit != 4 || settings.ProxyAccountStreamLimit != 8 ||
		settings.ProxyAccountStreamRecoveryReserve != 1 || settings.ProxyApiKeyFairShareCongestionThresholdPct != 0 ||
		settings.ProxyAccountStreamLimitOverride == nil || *settings.ProxyAccountStreamLimitOverride != 8 {
		t.Fatalf("default capacity settings: %+v %v", settings, err)
	}
	create, stream, reserve, fair := 0, 0, 3, 80
	settings.ProxyAccountResponseCreateLimitOverride = &create
	settings.ProxyAccountStreamLimitOverride = &stream
	settings.ProxyAccountStreamRecoveryReserveOverride = &reserve
	settings.ProxyApiKeyFairShareCongestionThresholdPctOverride = &fair
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_LB_PROXY_ACCOUNT_RESPONSE_CREATE_LIMIT", "6")
	t.Setenv("CODEX_LB_PROXY_ACCOUNT_STREAM_LIMIT", "12")
	t.Setenv("CODEX_LB_PROXY_ACCOUNT_STREAM_RECOVERY_RESERVE", "2")
	t.Setenv("CODEX_LB_PROXY_API_KEY_FAIR_SHARE_CONGESTION_THRESHOLD_PCT", "40")
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	settings, err = reopened.LoadSettings(ctx)
	if err != nil || settings.ProxyAccountStreamLimit != 0 || settings.ProxyAccountStreamLimitEnvironmentValue != 12 ||
		settings.ProxyApiKeyFairShareCongestionThresholdPct != 80 || settings.ProxyAccountResponseCreateLimit != 0 ||
		settings.ProxyAccountStreamRecoveryReserve != 3 {
		t.Fatalf("override was not retained after restart: %+v %v", settings, err)
	}
	settings.ProxyAccountResponseCreateLimitOverride = nil
	settings.ProxyAccountStreamLimitOverride = nil
	settings.ProxyAccountStreamRecoveryReserveOverride = nil
	settings.ProxyApiKeyFairShareCongestionThresholdPctOverride = nil
	if err := reopened.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	settings, err = reopened.LoadSettings(ctx)
	if err != nil || settings.ProxyAccountResponseCreateLimit != 6 || settings.ProxyAccountStreamLimit != 12 ||
		settings.ProxyAccountStreamRecoveryReserve != 2 || settings.ProxyApiKeyFairShareCongestionThresholdPct != 40 ||
		settings.ProxyAccountStreamLimitOverride != nil {
		t.Fatalf("cleared override did not inherit environment: %+v %v", settings, err)
	}
	bad := settings
	negative := -1
	bad.ProxyAccountStreamLimitOverride = &negative
	if err := reopened.SaveSettings(ctx, bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("negative cap accepted: %v", err)
	}
	bad = settings
	tooHigh := 101
	bad.ProxyApiKeyFairShareCongestionThresholdPctOverride = &tooHigh
	if err := reopened.SaveSettings(ctx, bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid fair-share threshold accepted: %v", err)
	}
	bad = settings
	tooSmall, tooLarge := 2, 3
	bad.ProxyAccountStreamLimitOverride, bad.ProxyAccountStreamRecoveryReserveOverride = &tooSmall, &tooLarge
	if err := reopened.SaveSettings(ctx, bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("recovery reserve larger than stream cap accepted: %v", err)
	}
}

func TestAccountAdmissionEnvironmentRejectsInvalidValues(t *testing.T) {
	for _, test := range []struct{ name, value string }{
		{"CODEX_LB_PROXY_ACCOUNT_STREAM_LIMIT", "-1"},
		{"CODEX_LB_PROXY_API_KEY_FAIR_SHARE_CONGESTION_THRESHOLD_PCT", "101"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(test.name, test.value)
			if _, err := Open(filepath.Join(t.TempDir(), "invalid.sqlite")); !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid environment accepted: %v", err)
			}
		})
	}
}

func TestAccountAdmissionV26UpgradeInheritsEnvironment(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v26.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:26] {
		if _, err := db.ExecContext(ctx, migration); err != nil {
			t.Fatal(err)
		}
	}
	for _, statement := range []string{fmt.Sprintf("PRAGMA application_id=%d", applicationID), "PRAGMA user_version=26"} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_LB_PROXY_ACCOUNT_STREAM_LIMIT", "12")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	settings, err := store.LoadSettings(ctx)
	if err != nil || settings.ProxyAccountStreamLimit != 12 || settings.ProxyAccountStreamLimitOverride != nil {
		t.Fatalf("V26 upgrade pinned capacity instead of inheriting environment: %+v %v", settings, err)
	}
}

func TestNewAccountAdmissionSettingsPinStartupEnvironment(t *testing.T) {
	t.Setenv("CODEX_LB_PROXY_ACCOUNT_RESPONSE_CREATE_LIMIT", "6")
	t.Setenv("CODEX_LB_PROXY_ACCOUNT_STREAM_LIMIT", "12")
	t.Setenv("CODEX_LB_PROXY_ACCOUNT_STREAM_RECOVERY_RESERVE", "2")
	t.Setenv("CODEX_LB_PROXY_API_KEY_FAIR_SHARE_CONGESTION_THRESHOLD_PCT", "40")
	store, path := testStore(t)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_LB_PROXY_ACCOUNT_RESPONSE_CREATE_LIMIT", "4")
	t.Setenv("CODEX_LB_PROXY_ACCOUNT_STREAM_LIMIT", "8")
	t.Setenv("CODEX_LB_PROXY_ACCOUNT_STREAM_RECOVERY_RESERVE", "1")
	t.Setenv("CODEX_LB_PROXY_API_KEY_FAIR_SHARE_CONGESTION_THRESHOLD_PCT", "0")
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	settings, err := reopened.LoadSettings(context.Background())
	if err != nil || settings.ProxyAccountResponseCreateLimit != 6 || settings.ProxyAccountStreamLimit != 12 ||
		settings.ProxyAccountStreamRecoveryReserve != 2 || settings.ProxyApiKeyFairShareCongestionThresholdPct != 40 ||
		settings.ProxyAccountStreamLimitEnvironmentValue != 8 {
		t.Fatalf("restart overwrote first-boot settings with a new environment: %+v %v", settings, err)
	}
}

func TestImportLegacyAccountAdmissionOverrides(t *testing.T) {
	t.Setenv("CODEX_LB_PROXY_ACCOUNT_RESPONSE_CREATE_LIMIT", "4")
	t.Setenv("CODEX_LB_PROXY_ACCOUNT_STREAM_LIMIT", "8")
	t.Setenv("CODEX_LB_PROXY_ACCOUNT_STREAM_RECOVERY_RESERVE", "1")
	t.Setenv("CODEX_LB_PROXY_API_KEY_FAIR_SHARE_CONGESTION_THRESHOLD_PCT", "0")
	ctx := context.Background()
	source, vault, _ := legacyFixture(t)
	legacy, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`ALTER TABLE dashboard_settings ADD COLUMN proxy_account_response_create_limit INTEGER`,
		`ALTER TABLE dashboard_settings ADD COLUMN proxy_account_stream_limit INTEGER`,
		`ALTER TABLE dashboard_settings ADD COLUMN proxy_account_stream_recovery_reserve INTEGER`,
		`ALTER TABLE dashboard_settings ADD COLUMN proxy_api_key_fair_share_congestion_threshold_pct INTEGER`,
		`UPDATE dashboard_settings SET proxy_account_response_create_limit=0,proxy_account_stream_limit=9,
 proxy_account_stream_recovery_reserve=NULL,proxy_api_key_fair_share_congestion_threshold_pct=80 WHERE id=1`,
	} {
		if _, err := legacy.ExecContext(ctx, statement); err != nil {
			legacy.Close()
			t.Fatal(err)
		}
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(filepath.Join(t.TempDir(), "go.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.ImportLegacySnapshot(ctx, source, vault); err != nil {
		t.Fatal(err)
	}
	settings, err := store.LoadSettings(ctx)
	if err != nil || settings.ProxyAccountResponseCreateLimit != 0 || settings.ProxyAccountResponseCreateLimitOverride == nil ||
		*settings.ProxyAccountResponseCreateLimitOverride != 0 || settings.ProxyAccountStreamLimit != 9 ||
		settings.ProxyAccountStreamRecoveryReserve != 1 || settings.ProxyAccountStreamRecoveryReserveOverride != nil ||
		settings.ProxyApiKeyFairShareCongestionThresholdPct != 80 {
		t.Fatalf("legacy capacity override/null lost: %+v %v", settings, err)
	}
}
