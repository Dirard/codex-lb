package updatefiles

import (
	"context"
	"path/filepath"
	"testing"
)

func TestStoreRestartKeepsRollbackUntilBootstrapActuallyChanges(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	bootstrap := filepath.Join(t.TempDir(), "bootstrap")
	writeExecutable(t, bootstrap, []byte("A"))
	state, err := store.Initialize(ctx, bootstrap, testDescriptor("go-v1.0.0"))
	if err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, bootstrap, []byte("B"))
	state, err = store.Initialize(ctx, bootstrap, testDescriptor("go-v1.1.0"))
	if err != nil || state.Previous == nil {
		t.Fatalf("manual upgrade failed: %v", err)
	}
	newer := state.Current
	state.Current, state.Previous, state.Phase = *state.Previous, &newer, "succeeded"
	if err := store.Save(ctx, state); err != nil {
		t.Fatal(err)
	}
	restarted, err := store.Initialize(ctx, bootstrap, testDescriptor("go-v1.1.0"))
	if err != nil || restarted.Current != state.Current || restarted.Previous == nil || *restarted.Previous != newer {
		t.Fatalf("repeated bootstrap undid rollback: %+v error=%v", restarted, err)
	}
	writeExecutable(t, bootstrap, []byte("C"))
	replaced, err := store.Initialize(ctx, bootstrap, testDescriptor("go-v1.2.0"))
	if err != nil || replaced.Current.Descriptor.Version != "go-v1.2.0" || replaced.BootstrapSHA256 != digestOf([]byte("C")) {
		t.Fatalf("genuinely new manual installation not adopted: %+v error=%v", replaced, err)
	}
}
