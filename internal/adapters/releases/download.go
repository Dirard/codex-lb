package releases

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"codex-lb/internal/domain"
)

// Download verifies the release descriptor against the fixed origin, fetches
// SHA256SUMS, streams the archive into the caller's private staging
// directory, validates its digest before extraction and extracts only the
// codex-lb regular file; known auxiliary entries are validated and skipped.
// The returned digest describes the extracted binary itself; no downloaded
// content is executed here.
func (s *Source) Download(ctx context.Context, release domain.RuntimeRelease, stagingDir string) (domain.RuntimeBinary, error) {
	release, err := s.validateRelease(release)
	if err != nil {
		return domain.RuntimeBinary{}, err
	}
	info, err := os.Stat(stagingDir)
	if err != nil {
		return domain.RuntimeBinary{}, fmt.Errorf("staging directory unavailable: %w", err)
	}
	if !info.IsDir() {
		return domain.RuntimeBinary{}, fmt.Errorf("staging path %q is not a directory", stagingDir)
	}
	expectedChecksum, err := s.archiveChecksum(ctx, release)
	if err != nil {
		return domain.RuntimeBinary{}, err
	}
	archivePath := filepath.Join(stagingDir, release.ArchiveName)
	if err := s.downloadArchive(ctx, release, archivePath, expectedChecksum); err != nil {
		return domain.RuntimeBinary{}, err
	}
	binaryPath := filepath.Join(stagingDir, binaryName)
	digest, err := extractBinary(archivePath, binaryPath, binaryLimit)
	_ = os.Remove(archivePath)
	if err != nil {
		return domain.RuntimeBinary{}, err
	}
	return domain.RuntimeBinary{Path: binaryPath, SHA256: digest}, nil
}

func (s *Source) archiveChecksum(ctx context.Context, release domain.RuntimeRelease) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, checksumTimeout)
	defer cancel()
	body, err := s.fetchBytes(ctx, release.ChecksumURL, binaryAccept, checksumLimit, "checksum file")
	if err != nil {
		return "", err
	}
	return parseChecksum(body, release.ArchiveName)
}

func parseChecksum(body []byte, archiveName string) (string, error) {
	digest := ""
	found := false
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return "", fmt.Errorf("malformed checksum line %q", line)
		}
		name := strings.TrimPrefix(fields[1], "*")
		if name != archiveName {
			continue
		}
		if found {
			return "", fmt.Errorf("duplicate checksum entry for %s", archiveName)
		}
		digest = strings.ToLower(fields[0])
		if !validChecksumDigest(digest) {
			return "", fmt.Errorf("invalid SHA-256 checksum %q", fields[0])
		}
		found = true
	}
	if !found {
		return "", fmt.Errorf("checksum file has no entry for %s", archiveName)
	}
	return digest, nil
}

func validChecksumDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func (s *Source) downloadArchive(ctx context.Context, release domain.RuntimeRelease, archivePath, expectedChecksum string) error {
	ctx, cancel := context.WithTimeout(ctx, archiveTimeout)
	defer cancel()
	req, err := newReleaseRequest(ctx, release.ArchiveURL, binaryAccept)
	if err != nil {
		return fmt.Errorf("invalid archive request: %w", err)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("archive download failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("archive download returned HTTP %d", resp.StatusCode)
	}
	created := false
	complete := false
	defer func() {
		if created && !complete {
			_ = os.Remove(archivePath)
		}
	}()
	file, err := os.OpenFile(archivePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("staged archive creation failed: %w", err)
	}
	created = true
	hasher := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hasher), io.LimitReader(resp.Body, archiveLimit+1))
	closeErr := file.Close()
	if copyErr != nil {
		return fmt.Errorf("archive download failed: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("staged archive close failed: %w", closeErr)
	}
	if written > archiveLimit {
		return fmt.Errorf("runtime archive exceeds %d bytes", archiveLimit)
	}
	if hex.EncodeToString(hasher.Sum(nil)) != expectedChecksum {
		return errors.New("runtime archive SHA-256 mismatch")
	}
	complete = true
	return nil
}
