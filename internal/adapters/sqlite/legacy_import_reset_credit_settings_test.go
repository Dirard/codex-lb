package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestImportLegacyResetCreditDisplaySettings(t *testing.T) {
	ctx := context.Background()
	source, vault, _ := legacyFixture(t)
	legacy, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`ALTER TABLE dashboard_settings ADD COLUMN show_reset_credit_badges INTEGER NOT NULL DEFAULT 1`,
		`ALTER TABLE dashboard_settings ADD COLUMN show_reset_credit_expiry_badge INTEGER NOT NULL DEFAULT 1`,
		`ALTER TABLE dashboard_settings ADD COLUMN auto_redeem_reset_credits_before_expiry INTEGER NOT NULL DEFAULT 0`,
		`UPDATE dashboard_settings SET show_reset_credit_badges=0,show_reset_credit_expiry_badge=0,auto_redeem_reset_credits_before_expiry=1 WHERE id=1`,
	} {
		if _, err := legacy.ExecContext(ctx, statement); err != nil {
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
	if err != nil || settings.ShowResetCreditBadges || settings.ShowResetCreditExpiryBadge {
		t.Fatalf("legacy display preferences lost: %+v %v", settings, err)
	}
}
