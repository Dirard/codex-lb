package updatefiles

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codex-lb/internal/domain"
)

func testDescriptor(version string) domain.RuntimeDescriptor {
	return domain.RuntimeDescriptor{Version: version, GOOS: "linux", GOARCH: "amd64", SchemaVersion: 33, UpdateProtocol: domain.RuntimeUpdateProtocol}
}

func digestOf(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func writeExecutable(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0o755); err != nil {
		t.Fatal(err)
	}
}

func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	dataDir := tempDataDir(t)
	store, err := New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, dataDir
}

// t.TempDir is group-accessible in this toolchain; update storage must not be.
func tempDataDir(t *testing.T) string {
	t.Helper()
	dataDir := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dataDir
}

func versionFile(dataDir, digest string) string {
	return filepath.Join(dataDir, "updates", "versions", digest, "codex-lb")
}

func stagedBinary(t *testing.T, store *Store, content []byte) domain.RuntimeBinary {
	t.Helper()
	dir, err := store.Stage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "codex-lb")
	writeExecutable(t, path, content)
	return domain.RuntimeBinary{Path: path, SHA256: digestOf(content)}
}

func TestStoreInitializeRoundTripAndStableDirectory(t *testing.T) {
	store, dataDir := newTestStore(t)
	executable := filepath.Join(t.TempDir(), "bootstrap")
	first := []byte("binary-one")
	writeExecutable(t, executable, first)
	state, err := store.Initialize(context.Background(), executable, testDescriptor("go-v1.0.0"))
	if err != nil {
		t.Fatal(err)
	}
	firstDigest := digestOf(first)
	if state.Current.SHA256 != firstDigest || state.Current.Path != versionFile(dataDir, firstDigest) || state.Phase != "idle" || state.Previous != nil || state.Pending != nil {
		t.Fatalf("unexpected initial state: %+v", state)
	}
	if err := store.Verify(context.Background(), state.Current); err != nil {
		t.Fatal(err)
	}
	if !MatchesExecutable(context.Background(), executable, firstDigest) || MatchesExecutable(context.Background(), executable, digestOf([]byte("another binary"))) {
		t.Fatal("bootstrap fallback did not require identical executable bytes")
	}
	loaded, err := store.Load(context.Background())
	if err != nil || loaded.Current != state.Current {
		t.Fatalf("state did not round-trip: %+v %v", loaded, err)
	}
	if _, err := store.Initialize(context.Background(), executable, testDescriptor("go-v1.0.0")); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Join(dataDir, "updates", "versions"))
	if len(entries) != 1 || entries[0].Name() != firstDigest {
		t.Fatalf("stable version directory was churned: %+v", entries)
	}
	if info, err := os.Stat(versionFile(dataDir, firstDigest)); err != nil || info.Mode().Perm() != 0o500 {
		t.Fatalf("installed executable permissions: %+v %v", info, err)
	}

	second := []byte("binary-two")
	writeExecutable(t, executable, second)
	state, err = store.Initialize(context.Background(), executable, testDescriptor("go-v1.0.4"))
	if err != nil {
		t.Fatal(err)
	}
	secondDigest := digestOf(second)
	if state.Current.SHA256 != secondDigest || state.Current.Descriptor.Version != "go-v1.0.4" {
		t.Fatalf("newer bootstrap was not adopted: %+v", state.Current)
	}
	if state.Previous == nil || state.Previous.SHA256 != firstDigest || state.Previous.Descriptor.Version != "go-v1.0.0" {
		t.Fatalf("previous version was not retained: %+v", state.Previous)
	}
	for _, digest := range []string{firstDigest, secondDigest} {
		if _, err := os.Stat(versionFile(dataDir, digest)); err != nil {
			t.Fatalf("retained version disappeared: %v", err)
		}
	}
	if loaded, err = store.Load(context.Background()); err != nil || loaded.Previous == nil || loaded.Previous.SHA256 != firstDigest {
		t.Fatalf("adopted state did not round-trip: %+v %v", loaded, err)
	}
}

func TestStoreRemovesOnlyOwnedInterruptedStages(t *testing.T) {
	store, dataDir := newTestStore(t)
	stage, err := store.Stage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	install, err := os.MkdirTemp(filepath.Join(dataDir, "updates", "versions"), ".install-")
	if err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(dataDir, "updates", "staging", "manual")
	if err := os.Mkdir(foreign, 0700); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	for _, path := range []string{stage, install} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("interrupted staging was not cleaned")
		}
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatal("cleanup removed an unowned directory")
	}
}

func TestStoreInstallRequiresStagedDigestVerifiedBinary(t *testing.T) {
	store, dataDir := newTestStore(t)
	content := []byte("release payload")
	binary := stagedBinary(t, store, content)
	installation, err := store.Install(context.Background(), binary, testDescriptor("go-v1.0.4"))
	if err != nil {
		t.Fatal(err)
	}
	digest := digestOf(content)
	if installation.SHA256 != digest || installation.Path != versionFile(dataDir, digest) {
		t.Fatalf("unexpected installation: %+v", installation)
	}
	installed, err := os.ReadFile(versionFile(dataDir, digest))
	if err != nil || string(installed) != string(content) {
		t.Fatalf("installed content mismatch: %q %v", installed, err)
	}
	if again, err := store.Install(context.Background(), binary, testDescriptor("go-v1.0.4")); err != nil || again.Path != installation.Path {
		t.Fatalf("stable install directory was not reused: %+v %v", again, err)
	}
	if err := store.Verify(context.Background(), installation); err != nil {
		t.Fatal(err)
	}

	foreign := filepath.Join(t.TempDir(), "codex-lb")
	writeExecutable(t, foreign, content)
	if _, err := store.Install(context.Background(), domain.RuntimeBinary{Path: foreign, SHA256: digest}, testDescriptor("go-v1.0.4")); err == nil || !strings.Contains(err.Error(), "invalid staged executable") {
		t.Fatalf("foreign path was installed: %v", err)
	}
	if _, err := store.Install(context.Background(), domain.RuntimeBinary{Path: binary.Path, SHA256: strings.ToUpper(digest)}, testDescriptor("go-v1.0.4")); err == nil || !strings.Contains(err.Error(), "invalid staged executable") {
		t.Fatalf("uppercase digest was installed: %v", err)
	}
	other := stagedBinary(t, store, []byte("tampered payload"))
	if _, err := store.Install(context.Background(), domain.RuntimeBinary{Path: other.Path, SHA256: digest}, testDescriptor("go-v1.0.4")); err == nil || !strings.Contains(err.Error(), "staged executable changed") {
		t.Fatalf("digest mismatch was installed: %v", err)
	}
	link := filepath.Join(filepath.Dir(other.Path), "link")
	if err := os.Symlink(other.Path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Install(context.Background(), domain.RuntimeBinary{Path: link, SHA256: digestOf([]byte("tampered payload"))}, testDescriptor("go-v1.0.4")); err == nil || !strings.Contains(err.Error(), "invalid executable") {
		t.Fatalf("symlink source was installed: %v", err)
	}
	versions, _ := os.ReadDir(filepath.Join(dataDir, "updates", "versions"))
	if len(versions) != 1 {
		t.Fatalf("rejected installs created version directories: %+v", versions)
	}
	store.DiscardStage(filepath.Dir(binary.Path))
	if _, err := os.Stat(filepath.Dir(binary.Path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned staging directory was not discarded: %v", err)
	}
	keep := filepath.Join(dataDir, "updates", "versions", "keep")
	if err := os.Mkdir(keep, 0o700); err != nil {
		t.Fatal(err)
	}
	store.DiscardStage(keep)
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("discard removed a non-staging path: %v", err)
	}
}

func TestStoreSaveRetainsCurrentPreviousPending(t *testing.T) {
	store, dataDir := newTestStore(t)
	versions := filepath.Join(dataDir, "updates", "versions")
	digests := []string{digestOf([]byte("one")), digestOf([]byte("two")), digestOf([]byte("three")), digestOf([]byte("orphan"))}
	for _, digest := range digests {
		if err := os.Mkdir(filepath.Join(versions, digest), 0o700); err != nil {
			t.Fatal(err)
		}
		writeExecutable(t, versionFile(dataDir, digest), []byte(digest))
	}
	waiting := domain.RuntimeInstallState{
		Current:  domain.RuntimeInstallation{Descriptor: testDescriptor("go-v1.0.1"), Path: versionFile(dataDir, digests[0]), SHA256: digests[0]},
		Previous: &domain.RuntimeInstallation{Descriptor: testDescriptor("go-v1.0.0"), Path: versionFile(dataDir, digests[1]), SHA256: digests[1]},
		Pending:  &domain.RuntimeInstallation{Descriptor: testDescriptor("go-v1.0.4"), Path: versionFile(dataDir, digests[2]), SHA256: digests[2]},
		Phase:    "waiting",
	}
	if err := store.Save(context.Background(), waiting); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background())
	if err != nil || loaded.Current != waiting.Current || loaded.Previous == nil || *loaded.Previous != *waiting.Previous || loaded.Pending == nil || *loaded.Pending != *waiting.Pending {
		t.Fatalf("pending state did not round-trip: %+v %v", loaded, err)
	}
	for _, digest := range digests {
		if _, err := os.Stat(versionFile(dataDir, digest)); err != nil {
			t.Fatalf("non-terminal save pruned %s: %v", digest, err)
		}
	}
	terminal := waiting
	terminal.Pending, terminal.Phase = nil, "succeeded"
	if err := store.Save(context.Background(), terminal); err != nil {
		t.Fatal(err)
	}
	for i, digest := range digests {
		_, err := os.Stat(versionFile(dataDir, digest))
		if i < 2 && err != nil {
			t.Fatalf("retained version %s disappeared: %v", digest, err)
		}
		if i >= 2 && !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unused version %s survived retention: %v", digest, err)
		}
	}
	invalid := terminal
	invalid.Current.SHA256 = strings.ToUpper(digests[0])
	if err := store.Save(context.Background(), invalid); err == nil || !strings.Contains(err.Error(), "invalid managed installation") {
		t.Fatalf("uppercase digest was saved: %v", err)
	}
	invalid.Current.SHA256 = digests[0]
	invalid.Current.Path = versionFile(dataDir, digests[3])
	if err := store.Save(context.Background(), invalid); err == nil {
		t.Fatal("mismatched path was saved")
	}
}

func TestStoreVerifyRejectsTamperedOrUnsafeExecutables(t *testing.T) {
	store, _ := newTestStore(t)
	content := []byte("verified executable")
	installation, err := store.Install(context.Background(), stagedBinary(t, store, content), testDescriptor("go-v1.0.4"))
	if err != nil {
		t.Fatal(err)
	}
	path := installation.Path
	if err := store.Verify(context.Background(), installation); err != nil {
		t.Fatal(err)
	}
	appendFile(t, path)
	if err := store.Verify(context.Background(), installation); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("tampered executable passed: %v", err)
	}
	restore(t, path, content, 0o644)
	if err := store.Verify(context.Background(), installation); err == nil || !strings.Contains(err.Error(), "unsafe managed executable") {
		t.Fatalf("public permissions passed: %v", err)
	}
	restore(t, path, content, 0o500)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), path); err != nil {
		t.Fatal(err)
	}
	if err := store.Verify(context.Background(), installation); err == nil || !strings.Contains(err.Error(), "unsafe managed executable") {
		t.Fatalf("symlink executable passed: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	restore(t, path, content, 0o500)
	if err := os.Chmod(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := store.Verify(context.Background(), installation); err == nil || !strings.Contains(err.Error(), "unsafe executable directory") {
		t.Fatalf("public version directory passed: %v", err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	installation.Path = filepath.Join(filepath.Dir(path), "elsewhere")
	if err := store.Verify(context.Background(), installation); err == nil || !strings.Contains(err.Error(), "invalid managed executable path") {
		t.Fatalf("mismatched path passed: %v", err)
	}
}

func appendFile(t *testing.T, path string) {
	t.Helper()
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o500); err != nil {
		t.Fatal(err)
	}
}

func restore(t *testing.T, path string, content []byte, mode os.FileMode) {
	t.Helper()
	if _, err := os.Lstat(path); err == nil {
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func TestStoreNewAndLoadRejectUnsafeLayouts(t *testing.T) {
	dataDir := tempDataDir(t)
	if err := os.MkdirAll(filepath.Join(dataDir, "updates"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := New(dataDir); err == nil || !strings.Contains(err.Error(), "private") {
		t.Fatalf("public updates directory was accepted: %v", err)
	}

	strictDir := tempDataDir(t)
	realUpdates := filepath.Join(strictDir, "real")
	if err := os.Mkdir(realUpdates, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(strictDir, "updates"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(strictDir, "updates")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realUpdates, filepath.Join(strictDir, "updates")); err != nil {
		t.Fatal(err)
	}
	if _, err := New(strictDir); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlinked updates directory was accepted: %v", err)
	}

	lockDir := tempDataDir(t)
	if err := os.MkdirAll(filepath.Join(lockDir, "updates", "versions"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realUpdates, filepath.Join(lockDir, "updates", "install.lock")); err != nil {
		t.Fatal(err)
	}
	if _, err := New(lockDir); err == nil || !strings.Contains(err.Error(), "unsafe update lock") {
		t.Fatalf("symlinked lock was accepted: %v", err)
	}

	shared := tempDataDir(t)
	first, err := New(shared)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(shared); err == nil || !strings.Contains(err.Error(), "already managed") {
		t.Fatalf("second manager was accepted: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := New(shared)
	if err != nil {
		t.Fatalf("lock was not released: %v", err)
	}
	defer second.Close()

	statePath := filepath.Join(shared, "updates", "state.json")
	digest := digestOf([]byte("state"))
	initial := domain.RuntimeInstallState{Current: domain.RuntimeInstallation{Descriptor: testDescriptor("go-v1.0.0"), SHA256: digest, Path: versionFile(shared, digest)}}
	data, err := json.Marshal(initial)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(statePath, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Load(context.Background()); err == nil || !strings.Contains(err.Error(), "unsafe installation state") {
		t.Fatalf("public state file was accepted: %v", err)
	}
	restore(t, statePath, []byte("not-json"), 0o600)
	if _, err := second.Load(context.Background()); err == nil || !strings.Contains(err.Error(), "invalid installation state") {
		t.Fatalf("invalid state JSON was accepted: %v", err)
	}
	tampered := domain.RuntimeInstallState{Current: domain.RuntimeInstallation{Descriptor: testDescriptor("go-v1.0.0"), SHA256: strings.ToUpper(digestOf([]byte("x")))}}
	data, _ = json.Marshal(tampered)
	restore(t, statePath, data, 0o600)
	if _, err := second.Load(context.Background()); err == nil || !strings.Contains(err.Error(), "invalid installation digest") {
		t.Fatalf("uppercase persisted digest was accepted: %v", err)
	}
	restore(t, statePath, make([]byte, 64<<10+1), 0o600)
	if _, err := second.Load(context.Background()); err == nil || !strings.Contains(err.Error(), "unsafe installation state") {
		t.Fatalf("oversized state file was accepted: %v", err)
	}
}
