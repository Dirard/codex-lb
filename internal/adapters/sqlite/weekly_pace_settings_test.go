package sqlite

import (
	"context"
	"database/sql"
	"testing"
)

func TestWeeklyPaceSettingsLegacyImport(t *testing.T) {
	ctx := context.Background()
	for _, invalid := range []bool{false, true} {
		t.Run(map[bool]string{false: "canonical CSV", true: "invalid days roll back"}[invalid], func(t *testing.T) {
			source, vault, _ := legacyFixture(t)
			legacy, err := sql.Open("sqlite", source)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := legacy.ExecContext(ctx, `ALTER TABLE dashboard_settings ADD COLUMN weekly_pace_working_days TEXT;
ALTER TABLE dashboard_settings ADD COLUMN weekly_pace_smoothing_minutes INTEGER;
UPDATE dashboard_settings SET weekly_pace_working_days='4, 0,4,2',weekly_pace_smoothing_minutes=240 WHERE id=1`); err != nil {
				legacy.Close()
				t.Fatal(err)
			}
			if invalid {
				if _, err := legacy.ExecContext(ctx, "UPDATE dashboard_settings SET weekly_pace_working_days='7' WHERE id=1"); err != nil {
					legacy.Close()
					t.Fatal(err)
				}
			}
			legacy.Close()
			store, _ := testStore(t)
			_, err = store.ImportLegacySnapshot(ctx, source, vault)
			if invalid {
				accounts, queryErr := store.ListAccounts(ctx)
				if err == nil || queryErr != nil || len(accounts) != 0 {
					t.Fatalf("invalid weekly settings did not roll back import: %v %+v %v", err, accounts, queryErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			settings, err := store.LoadSettings(ctx)
			if err != nil || settings.WeeklyPaceWorkingDays != "0,2,4" || settings.WeeklyPaceSmoothingMinutes != 240 {
				t.Fatalf("weekly settings lost on import: %+v %v", settings, err)
			}
		})
	}
}
