// Package updatefiles stores verified executables separately from runtime data.
package updatefiles

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"codex-lb/internal/domain"
	"golang.org/x/sys/unix"
)

const maxBinary = 256 << 20

type Store struct {
	root string
	lock *os.File
}

func New(dataDir string) (*Store, error) {
	for _, dir := range []string{dataDir, filepath.Join(dataDir, "updates"), filepath.Join(dataDir, "updates", "versions"), filepath.Join(dataDir, "updates", "staging")} {
		if err := privateDir(dir); err != nil {
			return nil, err
		}
	}
	root := filepath.Join(dataDir, "updates")
	lockPath := filepath.Join(root, "install.lock")
	if info, err := os.Lstat(lockPath); err == nil && (!info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0) {
		return nil, errors.New("unsafe update lock")
	}
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lock.Close()
		return nil, errors.New("runtime installation is already managed")
	}
	// No live operation can own staging files while we hold the install lock.
	for _, location := range []struct{ directory, prefix string }{{"staging", "stage-"}, {"versions", ".install-"}} {
		entries, _ := os.ReadDir(filepath.Join(root, location.directory))
		for _, entry := range entries {
			if entry.IsDir() && strings.HasPrefix(entry.Name(), location.prefix) {
				_ = os.RemoveAll(filepath.Join(root, location.directory, entry.Name()))
			}
		}
	}
	return &Store{root: root, lock: lock}, nil
}

func privateDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("update directory must be private and not a symlink")
	}
	return nil
}

func (s *Store) Close() error { return s.lock.Close() }

func validDigest(value string) bool {
	b, e := hex.DecodeString(value)
	return e == nil && len(b) == sha256.Size && strings.ToLower(value) == value
}
func (s *Store) path(digest string) string {
	return filepath.Join(s.root, "versions", digest, "codex-lb")
}

// Initialize preserves an installed managed version across restarts. A manually
// installed newer bootstrap release is adopted without rolling user data back.
func (s *Store) Initialize(ctx context.Context, executable string, descriptor domain.RuntimeDescriptor) (domain.RuntimeInstallState, error) {
	state, err := s.Load(ctx)
	if err == nil {
		// Re-running the same bootstrap must not undo an explicit rollback.
		// Only an actually replaced newer bootstrap represents a manual upgrade.
		if !domain.NewerRuntimeVersion(descriptor.Version, state.Current.Descriptor.Version) ||
			MatchesExecutable(ctx, executable, state.BootstrapSHA256) {
			return state, nil
		}
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return state, err
	}
	initial, err := s.installFile(ctx, executable, "", descriptor)
	if err != nil {
		return state, err
	}
	if state.Current.SHA256 != "" {
		previous := state.Current
		state.Previous = &previous
	}
	state.Current, state.Pending, state.Phase, state.LastError = initial, nil, "idle", ""
	state.BootstrapSHA256 = initial.SHA256
	return state, s.Save(ctx, state)
}

func (s *Store) Load(ctx context.Context) (domain.RuntimeInstallState, error) {
	var state domain.RuntimeInstallState
	if err := ctx.Err(); err != nil {
		return state, err
	}
	path := filepath.Join(s.root, "state.json")
	info, err := os.Lstat(path)
	if err != nil {
		return state, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 64<<10 {
		return state, errors.New("unsafe installation state")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return state, err
	}
	if err = json.Unmarshal(data, &state); err != nil {
		return state, errors.New("invalid installation state")
	}
	if state.BootstrapSHA256 != "" && !validDigest(state.BootstrapSHA256) {
		return state, errors.New("invalid bootstrap digest")
	}
	for _, item := range []*domain.RuntimeInstallation{&state.Current, state.Previous, state.Pending} {
		if item == nil {
			continue
		}
		if !validDigest(item.SHA256) {
			return state, errors.New("invalid installation digest")
		}
		item.Path = s.path(item.SHA256)
	}
	return state, nil
}

func (s *Store) Save(ctx context.Context, state domain.RuntimeInstallState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if state.BootstrapSHA256 != "" && !validDigest(state.BootstrapSHA256) {
		return errors.New("invalid bootstrap digest")
	}
	for _, item := range []*domain.RuntimeInstallation{&state.Current, state.Previous, state.Pending} {
		if item != nil && (!validDigest(item.SHA256) || item.Path != s.path(item.SHA256)) {
			return errors.New("invalid managed installation")
		}
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err = atomicWrite(s.root, "state.json", data); err != nil {
		return err
	}
	if state.Pending != nil || (state.Phase != "succeeded" && state.Phase != "failed") {
		return nil
	}
	// Only our hash-named, non-symlink version directories are retention targets.
	keep := map[string]bool{state.Current.SHA256: true}
	if state.Previous != nil {
		keep[state.Previous.SHA256] = true
	}
	entries, _ := os.ReadDir(filepath.Join(s.root, "versions"))
	for _, entry := range entries {
		if entry.IsDir() && validDigest(entry.Name()) && !keep[entry.Name()] {
			_ = os.RemoveAll(filepath.Join(s.root, "versions", entry.Name()))
		}
	}
	return nil
}

func atomicWrite(dir, name string, data []byte) error {
	file, err := os.CreateTemp(dir, ".state-")
	if err != nil {
		return err
	}
	path := file.Name()
	defer os.Remove(path)
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		return err
	}
	if err = os.Rename(path, filepath.Join(dir, name)); err != nil {
		return err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func (s *Store) Stage(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return os.MkdirTemp(filepath.Join(s.root, "staging"), "stage-")
}
func (s *Store) DiscardStage(dir string) {
	if filepath.Dir(dir) == filepath.Join(s.root, "staging") && strings.HasPrefix(filepath.Base(dir), "stage-") {
		_ = os.RemoveAll(dir)
	}
}

func (s *Store) Install(ctx context.Context, binary domain.RuntimeBinary, descriptor domain.RuntimeDescriptor) (domain.RuntimeInstallation, error) {
	if filepath.Dir(filepath.Dir(binary.Path)) != filepath.Join(s.root, "staging") || !validDigest(binary.SHA256) {
		return domain.RuntimeInstallation{}, errors.New("invalid staged executable")
	}
	return s.installFile(ctx, binary.Path, binary.SHA256, descriptor)
}

func (s *Store) installFile(ctx context.Context, path, expected string, descriptor domain.RuntimeDescriptor) (domain.RuntimeInstallation, error) {
	var item domain.RuntimeInstallation
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxBinary {
		return item, errors.New("invalid executable")
	}
	source, err := os.Open(path)
	if err != nil {
		return item, err
	}
	defer source.Close()
	opened, err := source.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return item, errors.New("executable changed while opening")
	}
	stage, err := os.MkdirTemp(filepath.Join(s.root, "versions"), ".install-")
	if err != nil {
		return item, err
	}
	defer os.RemoveAll(stage)
	destination, err := os.OpenFile(filepath.Join(stage, "codex-lb"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0500)
	if err != nil {
		return item, err
	}
	hash := sha256.New()
	count, err := io.Copy(io.MultiWriter(destination, hash), io.LimitReader(source, maxBinary+1))
	if err == nil {
		err = ctx.Err()
	}
	if err == nil && count > maxBinary {
		err = errors.New("executable too large")
	}
	if err == nil {
		err = destination.Sync()
	}
	err = errors.Join(err, destination.Close())
	if err != nil {
		return item, err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if expected != "" && digest != expected {
		return item, errors.New("staged executable changed")
	}
	item = domain.RuntimeInstallation{Descriptor: descriptor, Path: s.path(digest), SHA256: digest}
	stageDir, err := os.Open(stage)
	if err != nil {
		return item, err
	}
	if err = errors.Join(stageDir.Sync(), stageDir.Close()); err != nil {
		return item, err
	}
	final := filepath.Dir(item.Path)
	if _, err = os.Lstat(final); err == nil {
		return item, s.Verify(ctx, item)
	} else if !errors.Is(err, os.ErrNotExist) {
		return item, err
	}
	if err = os.Rename(stage, final); err != nil {
		return item, err
	}
	directory, err := os.Open(filepath.Dir(final))
	if err != nil {
		return item, err
	}
	defer directory.Close()
	return item, directory.Sync()
}

func (s *Store) Verify(ctx context.Context, item domain.RuntimeInstallation) error {
	if !validDigest(item.SHA256) || item.Path != s.path(item.SHA256) {
		return errors.New("invalid managed executable path")
	}
	directory, err := os.Lstat(filepath.Dir(item.Path))
	if err != nil || !directory.IsDir() || directory.Mode().Perm()&0077 != 0 {
		return errors.New("unsafe executable directory")
	}
	info, err := os.Lstat(item.Path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Mode().Perm()&0100 == 0 || info.Size() > maxBinary {
		return errors.New("unsafe managed executable")
	}
	file, err := os.Open(item.Path)
	if err != nil {
		return err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return errors.New("executable changed while opening")
	}
	hash := sha256.New()
	if _, err = io.Copy(hash, io.LimitReader(file, maxBinary+1)); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != item.SHA256 {
		return errors.New("executable checksum mismatch")
	}
	return nil
}

// MatchesExecutable permits direct serving on a noexec mount only when the
// already running bootstrap is exactly the committed managed executable.
func MatchesExecutable(ctx context.Context, path, digest string) bool {
	if !validDigest(digest) {
		return false
	}
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	hash := sha256.New()
	count, err := io.Copy(hash, io.LimitReader(file, maxBinary+1))
	return err == nil && ctx.Err() == nil && count <= maxBinary && hex.EncodeToString(hash.Sum(nil)) == digest
}
