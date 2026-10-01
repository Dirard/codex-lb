package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestImportLegacyRelativeAvailabilitySettings(t *testing.T) {
	ctx := context.Background()
	source, vault, _ := legacyFixture(t)
	legacy, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`ALTER TABLE dashboard_settings ADD COLUMN relative_availability_power REAL NOT NULL DEFAULT 2.0`,
		`ALTER TABLE dashboard_settings ADD COLUMN relative_availability_top_k INTEGER NOT NULL DEFAULT 5`,
		`UPDATE dashboard_settings SET relative_availability_power=3.25,relative_availability_top_k=9 WHERE id=1`,
	} {
		if _, err := legacy.ExecContext(ctx, statement); err != nil {
			legacy.Close()
			t.Fatal(err)
		}
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "go.sqlite")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ImportLegacySnapshot(ctx, source, vault); err != nil {
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
	settings, err := store.LoadSettings(ctx)
	if err != nil || settings.RelativeAvailabilityPower != 3.25 || settings.RelativeAvailabilityTopK != 9 {
		t.Fatalf("legacy relative availability lost: %+v %v", settings, err)
	}
}

func TestImportLegacyRelativeAvailabilityRejectsInvalidValue(t *testing.T) {
	ctx := context.Background()
	source, vault, _ := legacyFixture(t)
	legacy, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`ALTER TABLE dashboard_settings ADD COLUMN relative_availability_power REAL NOT NULL DEFAULT 2.0`,
		`ALTER TABLE dashboard_settings ADD COLUMN relative_availability_top_k INTEGER NOT NULL DEFAULT 5`,
		`UPDATE dashboard_settings SET relative_availability_power=0,relative_availability_top_k=21 WHERE id=1`,
	} {
		if _, err := legacy.ExecContext(ctx, statement); err != nil {
			legacy.Close()
			t.Fatal(err)
		}
	}
	_ = legacy.Close()
	store, err := Open(filepath.Join(t.TempDir(), "go.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.ImportLegacySnapshot(ctx, source, vault); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid legacy tuning accepted: %v", err)
	}
	var accounts int
	if err := store.db.QueryRowContext(ctx, "SELECT count(*) FROM accounts").Scan(&accounts); err != nil || accounts != 0 {
		t.Fatalf("partial import after invalid tuning: %d %v", accounts, err)
	}
}
