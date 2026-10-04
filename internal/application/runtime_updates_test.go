package application

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

func testDescriptor(version string) domain.RuntimeDescriptor {
	return domain.RuntimeDescriptor{Version: version, GOOS: "linux", GOARCH: "amd64", SchemaVersion: 33, UpdateProtocol: domain.RuntimeUpdateProtocol}
}

type fakeReleaseSource struct {
	mu          sync.Mutex
	release     domain.RuntimeRelease
	binary      domain.RuntimeBinary
	latestErr   error
	downloadErr error
	latestBlock chan struct{}
	latestCalls int
	downloads   int
}

func (f *fakeReleaseSource) Latest(context.Context) (domain.RuntimeRelease, error) {
	f.mu.Lock()
	f.latestCalls++
	block := f.latestBlock
	f.mu.Unlock()
	if block != nil {
		<-block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.release, f.latestErr
}

func (f *fakeReleaseSource) Download(_ context.Context, _ domain.RuntimeRelease, _ string) (domain.RuntimeBinary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.downloads++
	if f.downloadErr != nil {
		return domain.RuntimeBinary{}, f.downloadErr
	}
	return f.binary, nil
}

func (f *fakeReleaseSource) counters() (latest, downloads int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.latestCalls, f.downloads
}

type fakeInstallStore struct {
	mu         sync.Mutex
	states     []domain.RuntimeInstallState
	installErr error
	verifyErr  error
	discarded  []string
}

func (f *fakeInstallStore) Save(_ context.Context, state domain.RuntimeInstallState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.states = append(f.states, state)
	return nil
}

func (f *fakeInstallStore) Stage(context.Context) (string, error) { return "stage-dir", nil }

func (f *fakeInstallStore) Install(_ context.Context, binary domain.RuntimeBinary, descriptor domain.RuntimeDescriptor) (domain.RuntimeInstallation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return domain.RuntimeInstallation{Descriptor: descriptor, Path: "new-installed", SHA256: binary.SHA256}, f.installErr
}

func (f *fakeInstallStore) Verify(context.Context, domain.RuntimeInstallation) error {
	return f.verifyErr
}

func (f *fakeInstallStore) DiscardStage(dir string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.discarded = append(f.discarded, dir)
}

func (f *fakeInstallStore) lastState() domain.RuntimeInstallState {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.states) == 0 {
		return domain.RuntimeInstallState{}
	}
	return f.states[len(f.states)-1]
}

type fakeUpdateHost struct {
	mu              sync.Mutex
	descriptors     map[string]domain.RuntimeDescriptor
	waitErr         error
	waitBlock       chan struct{}
	stopErr         error
	backupErr       error
	prepareErrFor   string
	activateErrFor  string
	preparedVersion string
	calls           []string
	stops           int
}

func (h *fakeUpdateHost) record(call string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, call)
}

func (h *fakeUpdateHost) Inspect(_ context.Context, path string) (domain.RuntimeDescriptor, error) {
	h.record("inspect:" + path)
	h.mu.Lock()
	defer h.mu.Unlock()
	if descriptor, ok := h.descriptors[path]; ok {
		return descriptor, nil
	}
	return domain.RuntimeDescriptor{}, errors.New("unknown executable")
}

func (h *fakeUpdateHost) WaitForIdle(ctx context.Context) error {
	h.record("wait")
	if h.waitBlock != nil {
		select {
		case <-h.waitBlock:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return h.waitErr
}

func (h *fakeUpdateHost) Stop(context.Context) error {
	h.record("stop")
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stops++
	return h.stopErr
}

func (h *fakeUpdateHost) Backup(context.Context) error {
	h.record("backup")
	return h.backupErr
}

func (h *fakeUpdateHost) PrepareStart(_ context.Context, installation domain.RuntimeInstallation) error {
	h.record("prepare:" + installation.Descriptor.Version)
	h.mu.Lock()
	h.preparedVersion = installation.Descriptor.Version
	fail := h.prepareErrFor == "*" || installation.Descriptor.Version == h.prepareErrFor
	h.mu.Unlock()
	if fail {
		return errors.New("candidate failed startup verification")
	}
	return nil
}

func (h *fakeUpdateHost) Activate(context.Context) error {
	h.record("activate")
	h.mu.Lock()
	fail := h.preparedVersion == h.activateErrFor
	h.mu.Unlock()
	if fail {
		return errors.New("activation failed")
	}
	return nil
}

func (h *fakeUpdateHost) callLog() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.calls...)
}

func (h *fakeUpdateHost) stopCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.stops
}

func hasCall(calls []string, prefix string) bool {
	for _, call := range calls {
		if strings.HasPrefix(call, prefix) {
			return true
		}
	}
	return false
}

func newUpdateFixture(t *testing.T, cfg RuntimeUpdateConfig) (*RuntimeUpdates, *fakeReleaseSource, *fakeInstallStore, *fakeUpdateHost) {
	t.Helper()
	current := domain.RuntimeInstallation{Descriptor: testDescriptor("go-v1.0.0"), Path: "old-current", SHA256: strings.Repeat("a", 64)}
	newDescriptor := testDescriptor("go-v1.0.4")
	source := &fakeReleaseSource{release: domain.RuntimeRelease{Version: "go-v1.0.4"}, binary: domain.RuntimeBinary{Path: "stage-path", SHA256: strings.Repeat("b", 64)}}
	host := &fakeUpdateHost{descriptors: map[string]domain.RuntimeDescriptor{
		"old-current": current.Descriptor, "rollback-target": current.Descriptor,
		"stage-path": newDescriptor, "new-installed": newDescriptor,
	}}
	store := &fakeInstallStore{}
	updater := NewRuntimeUpdates(context.Background(), source, store, host, domain.RuntimeInstallState{Current: current}, cfg)
	t.Cleanup(func() { _ = updater.Close() })
	return updater, source, store, host
}

func setState(t *testing.T, updater *RuntimeUpdates, current domain.RuntimeInstallation, previous *domain.RuntimeInstallation) {
	t.Helper()
	updater.mu.Lock()
	updater.state.Current, updater.state.Previous = current, previous
	updater.mu.Unlock()
}

func waitForPhase(t *testing.T, updater *RuntimeUpdates, phase string) domain.RuntimeUpdateStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		status, err := updater.Status(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if status.Phase == phase {
			return status
		}
		if time.Now().After(deadline) {
			t.Fatalf("phase %q with error %q never reached %q", status.Phase, status.LastError, phase)
		}
		time.Sleep(time.Millisecond)
	}
}

func applyUpdate(t *testing.T, updater *RuntimeUpdates, version string) {
	t.Helper()
	if _, err := updater.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForPhase(t, updater, "idle")
	if _, err := updater.Apply(context.Background(), version); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeUpdatesManualCheckUpdatesStatus(t *testing.T) {
	updater, _, _, _ := newUpdateFixture(t, RuntimeUpdateConfig{})
	if _, err := updater.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	status := waitForPhase(t, updater, "idle")
	if status.CurrentVersion != "go-v1.0.0" || status.LatestVersion != "go-v1.0.4" || !status.UpdateAvailable || !status.Supported || status.CheckedAt == nil {
		t.Fatalf("unexpected status after manual check: %+v", status)
	}
}

func TestRuntimeUpdatesManualCheckFailureIsSanitized(t *testing.T) {
	updater, source, _, host := newUpdateFixture(t, RuntimeUpdateConfig{})
	source.latestErr = errors.New("secret upstream failure")
	if _, err := updater.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	status := waitForPhase(t, updater, "failed")
	if status.LastError != "Release check failed; try again later" || status.UpdateAvailable {
		t.Fatalf("check failure was not sanitized: %+v", status)
	}
	if host.stopCount() != 0 {
		t.Fatal("failed check touched the running host")
	}
}

func TestRuntimeUpdatesDuplicateCheckCoalescesAndCooldownSkipsFetch(t *testing.T) {
	updater, source, _, _ := newUpdateFixture(t, RuntimeUpdateConfig{})
	blocked := make(chan struct{})
	source.mu.Lock()
	source.latestBlock = blocked
	source.mu.Unlock()
	if _, err := updater.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForPhase(t, updater, "checking")
	status, err := updater.Check(context.Background())
	if err != nil || status.Phase != "checking" {
		t.Fatalf("duplicate manual check did not join the running check: %v %+v", err, status)
	}
	if _, err := updater.Apply(context.Background(), "go-v1.0.4"); !errors.Is(err, domain.ErrUpdateBusy) {
		t.Fatalf("operation was accepted while checking: %v", err)
	}
	close(blocked)
	waitForPhase(t, updater, "idle")
	if _, err := updater.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if latest, _ := source.counters(); latest != 1 {
		t.Fatalf("duplicate or cooled-down check fetched releases again: %d", latest)
	}
}

func TestRuntimeUpdatesAllowsOneOperationAtATime(t *testing.T) {
	updater, _, _, host := newUpdateFixture(t, RuntimeUpdateConfig{})
	blocked := make(chan struct{})
	host.waitBlock = blocked
	applyUpdate(t, updater, "go-v1.0.4")
	waitForPhase(t, updater, "waiting")
	for _, operation := range []func() error{
		func() error { _, err := updater.Apply(context.Background(), "go-v1.0.4"); return err },
		func() error { _, err := updater.Rollback(context.Background(), "go-v1.0.0"); return err },
		func() error { _, err := updater.Check(context.Background()); return err },
	} {
		if err := operation(); !errors.Is(err, domain.ErrUpdateBusy) {
			t.Fatalf("concurrent operation was accepted: %v", err)
		}
	}
	close(blocked)
	waitForPhase(t, updater, "succeeded")
}

func TestRuntimeUpdatesInstallationSurvivesCallerCancellation(t *testing.T) {
	updater, source, store, host := newUpdateFixture(t, RuntimeUpdateConfig{})
	if _, err := updater.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForPhase(t, updater, "idle")
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := updater.Apply(ctx, "go-v1.0.4"); err != nil {
		t.Fatal(err)
	}
	cancel()
	if status := waitForPhase(t, updater, "succeeded"); status.TargetVersion != "go-v1.0.4" {
		t.Fatalf("target was lost after caller cancellation: %+v", status)
	}
	calls := host.callLog()
	for _, expected := range []string{"stop", "backup", "prepare:go-v1.0.4", "activate"} {
		if !hasCall(calls, expected) {
			t.Fatalf("installation did not complete after caller cancellation: %+v", calls)
		}
	}
	state := store.lastState()
	if state.Current.Descriptor.Version != "go-v1.0.4" || state.Previous == nil || state.Previous.Descriptor.Version != "go-v1.0.0" || state.Pending != nil {
		t.Fatalf("final state was not switched: %+v", state)
	}
	if _, downloads := source.counters(); downloads != 1 || len(store.discarded) != 1 {
		t.Fatalf("stage was not used exactly once: downloads=%d discarded=%v", downloads, store.discarded)
	}
}

func TestRuntimeUpdatesWaitingTimeoutKeepsOldHostRunning(t *testing.T) {
	updater, _, store, host := newUpdateFixture(t, RuntimeUpdateConfig{WaitTimeout: 20 * time.Millisecond})
	host.waitBlock = make(chan struct{})
	applyUpdate(t, updater, "go-v1.0.4")
	status := waitForPhase(t, updater, "failed")
	if !strings.Contains(status.LastError, "safe switching window") {
		t.Fatalf("timeout was not reported as deferred: %+v", status)
	}
	if host.stopCount() != 0 || hasCall(host.callLog(), "backup") {
		t.Fatalf("deferred update touched the host: %+v", host.callLog())
	}
	if state := store.lastState(); state.Current.Descriptor.Version != "go-v1.0.0" || state.Pending != nil {
		t.Fatalf("deferred update changed installation state: %+v", state)
	}
}

func TestRuntimeUpdatesRejectsCandidateWithoutStoppingHost(t *testing.T) {
	cases := []struct {
		name     string
		prepare  func(*fakeReleaseSource, *fakeInstallStore, *fakeUpdateHost)
		fragment string
	}{
		{"download failure", func(s *fakeReleaseSource, _ *fakeInstallStore, _ *fakeUpdateHost) {
			s.downloadErr = errors.New("checksum mismatch")
		}, "Release download or integrity verification failed"},
		{"wrong version", func(_ *fakeReleaseSource, _ *fakeInstallStore, h *fakeUpdateHost) {
			h.descriptors["stage-path"] = testDescriptor("go-v9.9.9")
		}, "Release is incompatible"},
		{"incompatible schema", func(_ *fakeReleaseSource, _ *fakeInstallStore, h *fakeUpdateHost) {
			bad := testDescriptor("go-v1.0.4")
			bad.SchemaVersion = 99
			h.descriptors["stage-path"] = bad
		}, "Release is incompatible"},
		{"changed target", func(_ *fakeReleaseSource, _ *fakeInstallStore, h *fakeUpdateHost) {
			h.descriptors["new-installed"] = testDescriptor("go-v9.9.9")
		}, "Target executable is incompatible or changed"},
		{"install failure", func(_ *fakeReleaseSource, st *fakeInstallStore, _ *fakeUpdateHost) {
			st.installErr = errors.New("disk full")
		}, "Cannot save the verified release"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			updater, source, store, host := newUpdateFixture(t, RuntimeUpdateConfig{})
			tc.prepare(source, store, host)
			applyUpdate(t, updater, "go-v1.0.4")
			status := waitForPhase(t, updater, "failed")
			if !strings.Contains(status.LastError, tc.fragment) {
				t.Fatalf("unexpected failure %q, want %q", status.LastError, tc.fragment)
			}
			calls := host.callLog()
			if host.stopCount() != 0 || hasCall(calls, "backup") || hasCall(calls, "prepare") || hasCall(calls, "activate") {
				t.Fatalf("bad candidate stopped or switched the host: %+v", calls)
			}
			if state := store.lastState(); state.Current.Descriptor.Version != "go-v1.0.0" {
				t.Fatalf("bad candidate changed current state: %+v", state)
			}
		})
	}
}

func TestRuntimeUpdatesRecoversPreviousAfterHostFailures(t *testing.T) {
	cases := []struct {
		name    string
		failure func(*fakeUpdateHost)
	}{
		{"backup failure", func(h *fakeUpdateHost) { h.backupErr = errors.New("snapshot failed") }},
		{"startup failure", func(h *fakeUpdateHost) { h.prepareErrFor = "go-v1.0.4" }},
		{"activation failure", func(h *fakeUpdateHost) { h.activateErrFor = "go-v1.0.4" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			updater, _, store, host := newUpdateFixture(t, RuntimeUpdateConfig{})
			tc.failure(host)
			applyUpdate(t, updater, "go-v1.0.4")
			status := waitForPhase(t, updater, "failed")
			if !strings.Contains(status.LastError, "previous version restored") {
				t.Fatalf("recovery was not reported: %+v", status)
			}
			calls := host.callLog()
			if !hasCall(calls, "prepare:go-v1.0.0") || !hasCall(calls, "activate") {
				t.Fatalf("previous version was not restarted: %+v", calls)
			}
			state := store.lastState()
			if state.Current.Descriptor.Version != "go-v1.0.0" || state.Current.Path != "old-current" || state.Pending != nil {
				t.Fatalf("previous installation was not restored: %+v", state)
			}
		})
	}
}

func TestRuntimeUpdatesActivationFailureRetainsOriginalMetadata(t *testing.T) {
	updater, _, store, host := newUpdateFixture(t, RuntimeUpdateConfig{})
	previous := domain.RuntimeInstallation{Descriptor: testDescriptor("go-v1.0.0"), Path: "previous-a", SHA256: strings.Repeat("d", 64)}
	current := domain.RuntimeInstallation{Descriptor: testDescriptor("go-v1.0.1"), Path: "current-b", SHA256: strings.Repeat("e", 64)}
	setState(t, updater, current, &previous)
	host.activateErrFor = "go-v1.0.4"
	applyUpdate(t, updater, "go-v1.0.4")
	status := waitForPhase(t, updater, "failed")
	if !strings.Contains(status.LastError, "previous version restored") {
		t.Fatalf("activation failure was not recovered: %+v", status)
	}
	state := store.lastState()
	if state.Current != current || state.Previous == nil || *state.Previous != previous || state.Pending != nil {
		t.Fatalf("original current/previous metadata was not retained: %+v", state)
	}
	calls := host.callLog()
	if !hasCall(calls, "prepare:go-v1.0.1") || !hasCall(calls, "activate") {
		t.Fatalf("previous version was not reactivated: %+v", calls)
	}
}

func TestRuntimeUpdatesUnrecoverablePreviousRequiresManualRecovery(t *testing.T) {
	updater, _, store, host := newUpdateFixture(t, RuntimeUpdateConfig{})
	host.prepareErrFor = "*"
	applyUpdate(t, updater, "go-v1.0.4")
	status := waitForPhase(t, updater, "failed")
	if !strings.Contains(status.LastError, "manual recovery is required") {
		t.Fatalf("unrecoverable restore was hidden: %+v", status)
	}
	if state := store.lastState(); state.Current.Descriptor.Version != "go-v1.0.0" {
		t.Fatalf("unrecoverable restore changed persisted state: %+v", state)
	}
	if !hasCall(host.callLog(), "prepare:go-v1.0.0") {
		t.Fatalf("previous version was not attempted: %+v", host.callLog())
	}
}

func TestRuntimeUpdatesRollbackUsesRetainedPrevious(t *testing.T) {
	updater, source, store, host := newUpdateFixture(t, RuntimeUpdateConfig{})
	previous := domain.RuntimeInstallation{Descriptor: testDescriptor("go-v1.0.0"), Path: "rollback-target", SHA256: strings.Repeat("d", 64)}
	current := domain.RuntimeInstallation{Descriptor: testDescriptor("go-v1.0.4"), Path: "new-installed", SHA256: strings.Repeat("c", 64)}
	setState(t, updater, current, &previous)
	if _, err := updater.Rollback(context.Background(), "go-v9.9.9"); !errors.Is(err, domain.ErrUpdateTarget) {
		t.Fatalf("unknown rollback target was accepted: %v", err)
	}
	if _, err := updater.Rollback(context.Background(), "go-v1.0.0"); err != nil {
		t.Fatal(err)
	}
	status := waitForPhase(t, updater, "succeeded")
	if status.PreviousVersion != "go-v1.0.4" || status.CurrentVersion != "go-v1.0.0" {
		t.Fatalf("rollback status was not switched: %+v", status)
	}
	state := store.lastState()
	if state.Current != previous {
		t.Fatalf("rollback did not restore the retained installation: %+v", state.Current)
	}
	if state.Previous == nil || *state.Previous != current || state.Pending != nil {
		t.Fatalf("rollback did not retain the superseded version: %+v", state)
	}
	calls := host.callLog()
	if !hasCall(calls, "prepare:go-v1.0.0") || !hasCall(calls, "activate") || hasCall(calls, "prepare:go-v1.0.4") {
		t.Fatalf("rollback lifecycle mismatch: %+v", calls)
	}
	if _, downloads := source.counters(); downloads != 0 {
		t.Fatal("rollback downloaded from the release source")
	}
}

func TestRuntimeUpdatesRollbackRejectsChangedPrevious(t *testing.T) {
	updater, _, store, host := newUpdateFixture(t, RuntimeUpdateConfig{})
	previous := domain.RuntimeInstallation{Descriptor: testDescriptor("go-v1.0.0"), Path: "rollback-target", SHA256: strings.Repeat("d", 64)}
	current := domain.RuntimeInstallation{Descriptor: testDescriptor("go-v1.0.4"), Path: "new-installed", SHA256: strings.Repeat("c", 64)}
	setState(t, updater, current, &previous)
	store.verifyErr = errors.New("checksum mismatch")
	if _, err := updater.Rollback(context.Background(), "go-v1.0.0"); err != nil {
		t.Fatal(err)
	}
	status := waitForPhase(t, updater, "failed")
	if !strings.Contains(status.LastError, "Previous executable is missing or changed") {
		t.Fatalf("unexpected rollback failure: %+v", status)
	}
	if host.stopCount() != 0 {
		t.Fatalf("invalid rollback stopped the host: %+v", host.callLog())
	}
}

func TestRuntimeUpdatesCloseJoinsInFlightJob(t *testing.T) {
	updater, _, store, host := newUpdateFixture(t, RuntimeUpdateConfig{WaitTimeout: 2 * time.Second})
	host.waitBlock = make(chan struct{})
	applyUpdate(t, updater, "go-v1.0.4")
	waitForPhase(t, updater, "waiting")
	closed := make(chan error, 1)
	go func() { closed <- updater.Close() }()
	waitForPhase(t, updater, "failed")
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not join the in-flight installation")
	}
	if host.stopCount() != 0 {
		t.Fatalf("shutdown cancellation stopped the host: %+v", host.callLog())
	}
	if state := store.lastState(); state.Pending != nil {
		t.Fatalf("cancelled transition left a pending installation: %+v", state)
	}
}
