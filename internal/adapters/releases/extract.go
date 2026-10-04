package releases

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

var allowedAuxiliaryEntries = map[string]struct{}{
	"LICENSE":             {},
	"LICENSE.codex-relay": {},
	"codex-lb.service":    {},
}

// extractBinary unpacks only the root codex-lb regular file from a verified
// tar.gz archive. Published archives also contain known regular auxiliary
// entries (LICENSE, LICENSE.codex-relay, codex-lb.service); they are drained
// within a size bound but never written or executed. Absolute paths,
// traversal segments, links, other unexpected entries and duplicates are
// rejected; staging always receives a new 0600 file that becomes 0500 after
// the bounded write completes.
func extractBinary(archivePath, binaryPath string, limit int64) (string, error) {
	file, err := os.Open(archivePath)
	if err != nil {
		return "", fmt.Errorf("verified archive unavailable: %w", err)
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return "", fmt.Errorf("invalid runtime archive: %w", err)
	}
	defer gzipReader.Close()
	reader := tar.NewReader(gzipReader)
	digest := ""
	seen := make(map[string]struct{})
	created := false
	complete := false
	defer func() {
		if !complete && created {
			_ = os.Remove(binaryPath)
		}
	}()
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("invalid runtime archive: %w", err)
		}
		if err := validateArchiveEntry(header); err != nil {
			return "", err
		}
		if header.Typeflag == tar.TypeDir {
			continue
		}
		name := strings.TrimPrefix(header.Name, "./")
		if _, ok := seen[name]; ok {
			return "", fmt.Errorf("runtime archive contains duplicate %s entries", name)
		}
		seen[name] = struct{}{}
		if name == binaryName {
			digest, err = writeBinary(reader, binaryPath, limit)
			if err != nil {
				return "", err
			}
			created = true
		} else if err := skipAuxiliaryEntry(reader, name, auxiliaryLimit); err != nil {
			return "", err
		}
	}
	if digest == "" {
		return "", errors.New("runtime archive is missing codex-lb")
	}
	complete = true
	return digest, nil
}

func validateArchiveEntry(header *tar.Header) error {
	if isArchiveRoot(header.Name) {
		if header.Typeflag != tar.TypeDir {
			return fmt.Errorf("runtime archive entry %q is not a directory", header.Name)
		}
		return nil
	}
	if !isArchiveBinary(header.Name) && !isArchiveAuxiliary(header.Name) {
		return fmt.Errorf("runtime archive contains unexpected entry %q", header.Name)
	}
	if header.Typeflag != tar.TypeReg {
		return fmt.Errorf("runtime archive entry %q is not a regular file", header.Name)
	}
	return nil
}

func isArchiveAuxiliary(name string) bool {
	if _, ok := allowedAuxiliaryEntries[name]; ok {
		return true
	}
	trimmed, ok := strings.CutPrefix(name, "./")
	if !ok {
		return false
	}
	_, ok = allowedAuxiliaryEntries[trimmed]
	return ok
}

func skipAuxiliaryEntry(reader io.Reader, name string, limit int64) error {
	written, err := io.Copy(io.Discard, io.LimitReader(reader, limit+1))
	if err != nil {
		return fmt.Errorf("invalid runtime archive: %w", err)
	}
	if written > limit {
		return fmt.Errorf("auxiliary entry %s exceeds %d bytes", name, limit)
	}
	return nil
}

func isArchiveRoot(name string) bool {
	return name == "." || name == "./"
}

func isArchiveBinary(name string) bool {
	if name == binaryName {
		return true
	}
	trimmed, ok := strings.CutPrefix(name, "./")
	return ok && trimmed == binaryName
}

func writeBinary(reader io.Reader, binaryPath string, limit int64) (string, error) {
	file, err := os.OpenFile(binaryPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("staged binary creation failed: %w", err)
	}
	hasher := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hasher), io.LimitReader(reader, limit+1))
	closeErr := file.Close()
	if copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		_ = os.Remove(binaryPath)
		return "", fmt.Errorf("staged binary write failed: %w", copyErr)
	}
	if written > limit {
		_ = os.Remove(binaryPath)
		return "", fmt.Errorf("extracted %s exceeds %d bytes", binaryName, limit)
	}
	if err := os.Chmod(binaryPath, 0o500); err != nil {
		_ = os.Remove(binaryPath)
		return "", fmt.Errorf("staged binary permission failed: %w", err)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}
