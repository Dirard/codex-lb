package application

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"codex-lb/internal/domain"
)

func handoffDescriptor(version string, schema, floor int) domain.RuntimeDescriptor {
	descriptor := testDescriptor(version)
	descriptor.SchemaVersion, descriptor.Handoff, descriptor.SchemaFloor = schema, domain.RuntimeHandoffProtocol, floor
	return descriptor
}

func newSeamlessFixture(t *testing.T, target domain.RuntimeDescriptor) (*RuntimeUpdates, *fakeInstallStore, *fakeUpdateHost) {
	t.Helper()
	updater, _, store, host := newUpdateFixture(t, RuntimeUpdateConfig{})
	current := domain.RuntimeInstallation{Descriptor: handoffDescriptor("go-v1.0.0", 41, 40), Path: "old-current", SHA256: strings.Repeat("a", 64)}
	setState(t, updater, current, nil)
	host.mu.Lock()
	host.overlap = true
	host.descriptors["old-current"] = current.Descriptor
	host.descriptors["stage-path"], host.descriptors["new-installed"] = target, target
	host.mu.Unlock()
	return updater, store, host
}

func TestSeamlessUpdateNeverWaitsOrStops(t *testing.T) {
	t.Parallel()
	updater, store, host := newSeamlessFixture(t, handoffDescriptor("go-v1.0.4", 42, 40))
	if status, _ := updater.Status(context.Background()); !status.Seamless {
		t.Fatalf("status does not report seamless switching: %+v", status)
	}
	applyUpdate(t, updater, "go-v1.0.4")
	waitForPhase(t, updater, "succeeded")
	calls := host.callLog()
	for _, forbidden := range []string{"wait", "stop", "prepare:", "activate", "restore-backup"} {
		if hasCall(calls, forbidden) {
			t.Fatalf("seamless switch called %q: %v", forbidden, calls)
		}
	}
	order := []string{"backup", "beside:go-v1.0.4", "handover"}
	position := -1
	for _, call := range order {
		index := slices.Index(calls, call)
		if index <= position {
			t.Fatalf("calls %v do not contain %v in order", calls, order)
		}
		position = index
	}
	state := store.lastState()
	if state.Current.Descriptor.Version != "go-v1.0.4" || state.Previous == nil || state.Previous.Descriptor.Version != "go-v1.0.0" || state.Phase != "succeeded" {
		t.Fatalf("unexpected committed state: %+v", state)
	}
}

func TestSeamlessCandidateFailureKeepsCurrentWithoutRestore(t *testing.T) {
	t.Parallel()
	updater, store, host := newSeamlessFixture(t, handoffDescriptor("go-v1.0.4", 42, 40))
	host.besideErr = errors.New("candidate failed")
	applyUpdate(t, updater, "go-v1.0.4")
	status := waitForPhase(t, updater, "failed")
	if !strings.Contains(status.LastError, "current version keeps serving") || status.CurrentVersion != "go-v1.0.0" {
		t.Fatalf("unexpected failure status: %+v", status)
	}
	if calls := host.callLog(); hasCall(calls, "restore-backup") || hasCall(calls, "stop") || hasCall(calls, "handover") {
		t.Fatalf("failed candidate disturbed the running worker: %v", calls)
	}
	if state := store.lastState(); state.Current.Descriptor.Version != "go-v1.0.0" || state.Pending != nil {
		t.Fatalf("failed candidate changed the selection: %+v", state)
	}
}

func TestSeamlessHandoverFailureRestoresSelection(t *testing.T) {
	t.Parallel()
	updater, store, host := newSeamlessFixture(t, handoffDescriptor("go-v1.0.4", 41, 40))
	host.handoverErr = errors.New("activation failed")
	applyUpdate(t, updater, "go-v1.0.4")
	waitForPhase(t, updater, "failed")
	if hasCall(host.callLog(), "discard") {
		t.Fatal("a candidate stopped by Handover must not be discarded twice")
	}
	if state := store.lastState(); state.Current.Descriptor.Version != "go-v1.0.0" || state.Phase != "failed" {
		t.Fatalf("handover failure did not restore the selection: %+v", state)
	}
}

func TestBreakingMigrationFallsBackToClassicSwitch(t *testing.T) {
	t.Parallel()
	updater, _, host := newSeamlessFixture(t, handoffDescriptor("go-v1.0.4", 43, 43))
	applyUpdate(t, updater, "go-v1.0.4")
	waitForPhase(t, updater, "succeeded")
	calls := host.callLog()
	if hasCall(calls, "beside:") || !hasCall(calls, "wait") || !hasCall(calls, "prepare:go-v1.0.4") {
		t.Fatalf("breaking migration did not use the classic switch: %v", calls)
	}
}

func TestAdditiveSchemaRollbackNeedsNoRestore(t *testing.T) {
	t.Parallel()
	updater, _, host := newSeamlessFixture(t, handoffDescriptor("go-v1.0.4", 42, 40))
	current := domain.RuntimeInstallation{Descriptor: handoffDescriptor("go-v1.0.4", 42, 40), Path: "new-installed", SHA256: strings.Repeat("b", 64)}
	previous := domain.RuntimeInstallation{Descriptor: handoffDescriptor("go-v1.0.0", 41, 40), Path: "old-current", SHA256: strings.Repeat("a", 64)}
	setState(t, updater, current, &previous)
	status, _ := updater.Status(context.Background())
	if !status.CanRollback || status.RollbackNote != "" {
		t.Fatalf("additive schema rollback not offered: %+v", status)
	}
	if _, err := updater.Rollback(context.Background(), "go-v1.0.0"); err != nil {
		t.Fatal(err)
	}
	waitForPhase(t, updater, "succeeded")
	if calls := host.callLog(); !hasCall(calls, "beside:go-v1.0.0") || hasCall(calls, "restore-backup") {
		t.Fatalf("rollback was not a seamless switch without restore: %v", calls)
	}
}
