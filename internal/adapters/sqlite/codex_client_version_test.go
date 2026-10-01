package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"codex-lb/internal/domain"
)

func TestCodexClientVersionUpgradePersistenceAndValidation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v32.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:32] {
		if _, err := db.ExecContext(ctx, migration); err != nil {
			t.Fatal(err)
		}
	}
	for _, statement := range []string{
		"UPDATE runtime_settings SET prohibit_fast_mode=1 WHERE id=1",
		fmt.Sprintf("PRAGMA application_id=%d", applicationID), "PRAGMA user_version=32",
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
	if err != nil || settings.CodexClientVersion != domain.DefaultCodexClientVersion || !settings.ProhibitFastMode {
		t.Fatalf("version upgrade changed existing settings: %+v %v", settings, err)
	}
	settings.CodexClientVersion = " 0.157.0-alpha.12 "
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSettings(ctx, settings); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale setting accepted: %v", err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if version, err := reopened.LoadCodexClientVersion(ctx); err != nil || version != "0.157.0-alpha.12" {
		t.Fatalf("version lost after restart: %q %v", version, err)
	}
	settings, err = reopened.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"", "latest", "0.157", "v0.157.0", "0.157.0/evil", "0.157.0\r\nX-Injected: yes", "0.157.0-" + strings.Repeat("a", 64)} {
		settings.CodexClientVersion = invalid
		if err := reopened.SaveSettings(ctx, settings); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid version accepted: %q %v", invalid, err)
		}
	}
	if version, err := reopened.LoadCodexClientVersion(ctx); err != nil || version != "0.157.0-alpha.12" {
		t.Fatalf("rejections changed version: %q %v", version, err)
	}
}
