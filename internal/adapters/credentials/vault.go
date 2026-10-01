// Package credentials stores secrets using the legacy-compatible Fernet format.
package credentials

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/fernet/fernet-go"
)

// Includes bounded encrypted operational continuation state. Account/OAuth
// input limits are enforced at their HTTP boundary independently.
const maxSecretBytes = 16 << 20

var ErrCiphertext = errors.New("credential cannot be decrypted with the configured encryption key")

type Vault struct {
	key         *fernet.Key
	fingerprint string
}

// Open never replaces an existing key. Creation must only be enabled for a new
// store: losing a key must not silently make existing credentials unreadable.
func Open(path string, create bool) (*Vault, error) {
	if path == "" {
		return nil, errors.New("encryption key path is required")
	}
	if create {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, fmt.Errorf("create encryption key directory: %w", err)
		}
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			var key fernet.Key
			if err = key.Generate(); err == nil {
				_, err = io.WriteString(file, key.Encode())
			}
			if err == nil {
				err = file.Sync()
			}
			err = errors.Join(err, file.Close())
			if err != nil {
				return nil, errors.New("could not persist encryption key; inspect the key file before retrying")
			}
		} else if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("create encryption key: %w", err)
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("open encryption key: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("encryption key must be a private regular file (mode 0600)")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open encryption key: %w", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("encryption key changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(raw) > 4096 {
		return nil, errors.New("cannot read encryption key")
	}
	key, err := fernet.DecodeKey(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, errors.New("invalid encryption key format")
	}
	// Legacy fingerprints the exact file bytes, including a trailing newline.
	return &Vault{key: key, fingerprint: fmt.Sprintf("sha256:%x", sha256.Sum256(raw))}, nil
}

func (v *Vault) Fingerprint() string { return v.fingerprint }

func (v *Vault) Encrypt(plain []byte) ([]byte, error) {
	if len(plain) > maxSecretBytes {
		return nil, errors.New("credential exceeds size limit")
	}
	token, err := fernet.EncryptAndSign(plain, v.key)
	if err != nil {
		return nil, errors.New("could not encrypt credential")
	}
	return token, nil
}

func (v *Vault) Decrypt(token []byte) ([]byte, error) {
	if len(token) > 2*maxSecretBytes {
		return nil, ErrCiphertext
	}
	// Stored credentials have no Fernet TTL; OAuth expiry is checked separately.
	plain := fernet.VerifyAndDecrypt(token, 0, []*fernet.Key{v.key})
	if plain == nil || len(plain) > maxSecretBytes {
		return nil, ErrCiphertext
	}
	return plain, nil
}
