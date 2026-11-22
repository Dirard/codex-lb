package sqlite

import (
	"context"
	"errors"
	"testing"

	"codex-lb/internal/domain"
)

func TestAntigravityClientVersionSettingPersistsAndValidates(t *testing.T) {
	ctx := context.Background()
	store, path := testStore(t)
	if version, err := store.LoadAntigravityClientVersion(ctx); err != nil || version != domain.DefaultAntigravityClientVersion {
		t.Fatalf("default version: %q %v", version, err)
	}
	settings, err := store.LoadSettings(ctx)
	if err != nil || settings.AntigravityClientVersion != domain.DefaultAntigravityClientVersion {
		t.Fatalf("settings default: %+v %v", settings.AntigravityClientVersion, err)
	}
	settings.AntigravityClientVersion = " 2.35.0 "
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if version, err := reopened.LoadAntigravityClientVersion(ctx); err != nil || version != "2.35.0" {
		t.Fatalf("version lost after restart: %q %v", version, err)
	}
	settings, err = reopened.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"latest", "2.35", "v2.35.0", "2.35.0-beta", "2.35.0\r\nX-Injected: yes", "12345.1.1"} {
		settings.AntigravityClientVersion = invalid
		if err := reopened.SaveSettings(ctx, settings); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid version accepted: %q %v", invalid, err)
		}
	}
	if version, err := reopened.LoadAntigravityClientVersion(ctx); err != nil || version != "2.35.0" {
		t.Fatalf("rejections changed the version: %q %v", version, err)
	}
}
