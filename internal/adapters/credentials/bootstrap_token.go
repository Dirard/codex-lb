package credentials

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// BootstrapTokenFile stores a generated one-time dashboard setup token in a
// private file inside the data directory. The value is never logged.
type BootstrapTokenFile struct {
	path string
}

func NewBootstrapTokenFile(path string) *BootstrapTokenFile {
	return &BootstrapTokenFile{path: path}
}

func (f *BootstrapTokenFile) Location() string { return f.path }

// Publish replaces the file atomically with a 0600 file containing token.
func (f *BootstrapTokenFile) Publish(token string) error {
	dir := filepath.Dir(f.path)
	temp, err := os.CreateTemp(dir, ".dashboard-bootstrap-token-*")
	if err != nil {
		return fmt.Errorf("create bootstrap token file: %w", err)
	}
	name := temp.Name()
	defer func() { _ = os.Remove(name) }()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.WriteString(token + "\n"); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(name, f.path)
}

// Withdraw removes a published token; a missing file is not an error.
func (f *BootstrapTokenFile) Withdraw() error {
	if err := os.Remove(f.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove bootstrap token file: %w", err)
	}
	return nil
}
