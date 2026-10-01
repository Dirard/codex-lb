package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"codex-lb/internal/adapters/credentials"
)

func TestRuntimeKeyValidation(t *testing.T) {
	store, path := testStore(t)
	ctx := context.Background()
	key, err := credentials.Open(filepath.Join(t.TempDir(), "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateRuntime(ctx, key); err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateRuntime(ctx, key); err != nil {
		t.Fatal(err)
	}
	other, err := credentials.Open(filepath.Join(t.TempDir(), "other"), true)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateRuntime(ctx, other); err == nil {
		t.Fatal("replacement key accepted")
	}
	store.Close()
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.ValidateRuntime(ctx, key); err != nil {
		t.Fatal(err)
	}
	// Malformed credential rows must not become ready even with the right key.
	saveTestAccount(t, store, "broken")
	if err := store.ValidateRuntime(ctx, key); err == nil {
		t.Fatal("invalid ciphertext accepted")
	}
}
