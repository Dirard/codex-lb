package releases

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codex-lb/internal/domain"
)

type archiveEntry struct {
	name     string
	typeflag byte
	body     []byte
	linkname string
}

func buildArchive(t *testing.T, entries ...archiveEntry) []byte {
	t.Helper()
	var raw bytes.Buffer
	gzipWriter := gzip.NewWriter(&raw)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		header := &tar.Header{
			Typeflag: entry.typeflag,
			Name:     entry.name,
			Linkname: entry.linkname,
			Mode:     0o755,
		}
		if entry.typeflag == tar.TypeReg {
			header.Size = int64(len(entry.body))
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if entry.typeflag == tar.TypeReg && len(entry.body) > 0 {
			if _, err := tarWriter.Write(entry.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}

func checksumBody(t *testing.T, archiveName string, archive []byte) io.Reader {
	t.Helper()
	sum := sha256.Sum256(archive)
	return strings.NewReader(hex.EncodeToString(sum[:]) + "  " + archiveName + "\n")
}

func staticChecksumBody(digest, archiveName string) io.Reader {
	return strings.NewReader(digest + "  " + archiveName + "\n")
}

func downloadTransport(release domain.RuntimeRelease, checksumBody io.Reader, archive io.Reader) *stubTransport {
	return &stubTransport{responses: map[string]stubResponse{
		release.ChecksumURL: {status: http.StatusOK, body: checksumBody},
		release.ArchiveURL:  {status: http.StatusOK, body: archive},
	}}
}

func assertDownloadFailsClean(t *testing.T, release domain.RuntimeRelease, transport *stubTransport, staging string, fragment string) {
	t.Helper()
	_, err := newTestSource(transport).Download(context.Background(), release, staging)
	if err == nil || !strings.Contains(err.Error(), fragment) {
		t.Fatalf("expected failure containing %q, got %v", fragment, err)
	}
	entries, readErr := os.ReadDir(staging)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("failed download left staged files: %+v", entries)
	}
}

func TestDownloadVerifiesAndExtractsBinary(t *testing.T) {
	binaryContent := []byte("fake codex binary")
	archive := buildArchive(t,
		archiveEntry{name: "./", typeflag: tar.TypeDir},
		archiveEntry{name: "codex-lb", typeflag: tar.TypeReg, body: binaryContent},
	)
	release := testRelease("go-v1.2.0")
	transport := downloadTransport(release, checksumBody(t, release.ArchiveName, archive), bytes.NewReader(archive))
	staging := t.TempDir()
	binary, err := newTestSource(transport).Download(context.Background(), release, staging)
	if err != nil {
		t.Fatal(err)
	}
	binaryPath := filepath.Join(staging, binaryName)
	sum := sha256.Sum256(binaryContent)
	if binary.Path != binaryPath || binary.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("binary = %+v; want path %q digest %s", binary, binaryPath, hex.EncodeToString(sum[:]))
	}
	content, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(content, binaryContent) {
		t.Fatalf("extracted content = %q", content)
	}
	info, err := os.Stat(binaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o500 {
		t.Fatalf("binary mode = %o; want 0500", info.Mode().Perm())
	}
	entries, err := os.ReadDir(staging)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != binaryName {
		t.Fatalf("staging entries = %+v", entries)
	}
	requests := transport.recorded()
	if len(requests) != 2 {
		t.Fatalf("expected checksum and archive requests, got %+v", requests)
	}
	for _, request := range requests {
		if request.authorization != "" || request.cookie != "" {
			t.Fatalf("download sent credentials: %+v", request)
		}
	}
}

func TestDownloadAcceptsPublishedReleaseArchive(t *testing.T) {
	// Exact composition observed in real Dirard/codex-lb release archives.
	binaryContent := []byte("published codex binary")
	archive := buildArchive(t,
		archiveEntry{name: "codex-lb", typeflag: tar.TypeReg, body: binaryContent},
		archiveEntry{name: "LICENSE", typeflag: tar.TypeReg, body: []byte("codex-lb license")},
		archiveEntry{name: "LICENSE.codex-relay", typeflag: tar.TypeReg, body: []byte("relay license")},
		archiveEntry{name: "./codex-lb.service", typeflag: tar.TypeReg, body: []byte("[Unit]\nDescription=codex-lb\n")},
	)
	release := testRelease("go-v1.0.4")
	transport := downloadTransport(release, checksumBody(t, release.ArchiveName, archive), bytes.NewReader(archive))
	staging := t.TempDir()
	binary, err := newTestSource(transport).Download(context.Background(), release, staging)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(binaryContent)
	if binary.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("published binary digest = %s", binary.SHA256)
	}
	entries, err := os.ReadDir(staging)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != binaryName {
		t.Fatalf("auxiliary entries must not be extracted, staging = %+v", entries)
	}
}

func TestDownloadRejectsChecksumFailures(t *testing.T) {
	release := testRelease("go-v1.2.0")
	archive := buildArchive(t, archiveEntry{name: "codex-lb", typeflag: tar.TypeReg, body: []byte("binary")})
	cases := []struct {
		name     string
		checksum io.Reader
		fragment string
	}{
		{"mismatch", staticChecksumBody(strings.Repeat("0", 64), release.ArchiveName), "SHA-256 mismatch"},
		{"missing entry", strings.NewReader(strings.Repeat("a", 64) + "  other.tar.gz\n"), "no entry"},
		{"malformed digest", staticChecksumBody("zz", release.ArchiveName), "invalid SHA-256 checksum"},
		{"malformed line", strings.NewReader("nope\n"), "malformed checksum line"},
		{"duplicate entry", strings.NewReader(strings.Repeat("b", 64) + "  " + release.ArchiveName + "\n" + strings.Repeat("c", 64) + "  " + release.ArchiveName + "\n"), "duplicate checksum entry"},
		{"oversized", &repeatReader{byteValue: 'a', remaining: checksumLimit + 1}, "checksum file exceeds"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			transport := downloadTransport(release, tc.checksum, bytes.NewReader(archive))
			assertDownloadFailsClean(t, release, transport, t.TempDir(), tc.fragment)
		})
	}
}

func TestDownloadRejectsCorruptAndOversizedArchives(t *testing.T) {
	release := testRelease("go-v1.2.0")
	corrupt := []byte("not a gzip stream")
	transport := downloadTransport(release, checksumBody(t, release.ArchiveName, corrupt), bytes.NewReader(corrupt))
	assertDownloadFailsClean(t, release, transport, t.TempDir(), "invalid runtime archive")

	oversized := &repeatReader{byteValue: 'x', remaining: archiveLimit + 2}
	transport = downloadTransport(release, staticChecksumBody(strings.Repeat("0", 64), release.ArchiveName), oversized)
	assertDownloadFailsClean(t, release, transport, t.TempDir(), "runtime archive exceeds")
}

func TestDownloadRejectsUnsafeArchiveEntries(t *testing.T) {
	body := []byte("binary")
	cases := []struct {
		name     string
		entries  []archiveEntry
		fragment string
	}{
		{"traversal", []archiveEntry{{name: "../evil", typeflag: tar.TypeReg, body: body}}, "unexpected entry"},
		{"absolute", []archiveEntry{{name: "/codex-lb", typeflag: tar.TypeReg, body: body}}, "unexpected entry"},
		{"nested traversal", []archiveEntry{{name: "ok/../codex-lb", typeflag: tar.TypeReg, body: body}}, "unexpected entry"},
		{"symlink", []archiveEntry{{name: "codex-lb", typeflag: tar.TypeSymlink, linkname: "/bin/sh"}}, "not a regular file"},
		{"hardlink", []archiveEntry{{name: "codex-lb", typeflag: tar.TypeLink, linkname: "/etc/passwd"}}, "not a regular file"},
		{"auxiliary symlink", []archiveEntry{{name: "LICENSE", typeflag: tar.TypeSymlink, linkname: "/etc/passwd"}}, "not a regular file"},
		{"auxiliary duplicate", []archiveEntry{
			{name: "LICENSE", typeflag: tar.TypeReg, body: body},
			{name: "./LICENSE", typeflag: tar.TypeReg, body: body},
		}, "duplicate LICENSE entries"},
		{"root file", []archiveEntry{{name: ".", typeflag: tar.TypeReg, body: body}}, "not a directory"},
		{"duplicate binary", []archiveEntry{
			{name: "codex-lb", typeflag: tar.TypeReg, body: body},
			{name: "./codex-lb", typeflag: tar.TypeReg, body: body},
		}, "duplicate codex-lb"},
		{"unexpected file", []archiveEntry{
			{name: "codex-lb", typeflag: tar.TypeReg, body: body},
			{name: "notes.txt", typeflag: tar.TypeReg, body: body},
		}, "unexpected entry"},
		{"missing binary", []archiveEntry{{name: ".", typeflag: tar.TypeDir}}, "missing codex-lb"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			release := testRelease("go-v1.2.0")
			archive := buildArchive(t, tc.entries...)
			transport := downloadTransport(release, checksumBody(t, release.ArchiveName, archive), bytes.NewReader(archive))
			assertDownloadFailsClean(t, release, transport, t.TempDir(), tc.fragment)
		})
	}
}

func TestDownloadRejectsOversizedAuxiliaryEntry(t *testing.T) {
	release := testRelease("go-v1.0.4")
	archive := buildArchive(t,
		archiveEntry{name: "codex-lb", typeflag: tar.TypeReg, body: []byte("binary")},
		archiveEntry{name: "LICENSE", typeflag: tar.TypeReg, body: bytes.Repeat([]byte("l"), auxiliaryLimit+2)},
	)
	transport := downloadTransport(release, checksumBody(t, release.ArchiveName, archive), bytes.NewReader(archive))
	assertDownloadFailsClean(t, release, transport, t.TempDir(), "auxiliary entry LICENSE exceeds")
}

func TestDownloadFollowsOnlyGitHubHTTPSRedirects(t *testing.T) {
	binaryContent := []byte("redirected binary")
	archive := buildArchive(t, archiveEntry{name: "codex-lb", typeflag: tar.TypeReg, body: binaryContent})
	release := testRelease("go-v1.2.0")
	assetURL := "https://release-assets.githubusercontent.com/Dirard/codex-lb/123/codex-lb-go-v1.2.0-linux-amd64.tar.gz"
	transport := &stubTransport{responses: map[string]stubResponse{
		release.ChecksumURL: {status: http.StatusOK, body: checksumBody(t, release.ArchiveName, archive)},
		release.ArchiveURL:  {status: http.StatusFound, header: map[string]string{"Location": assetURL}, body: strings.NewReader("")},
		assetURL:            {status: http.StatusOK, body: bytes.NewReader(archive)},
	}}
	binary, err := newTestSource(transport).Download(context.Background(), release, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(binaryContent)
	if binary.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("redirected binary digest = %s", binary.SHA256)
	}
	requests := transport.recorded()
	if len(requests) != 3 || requests[2].authorization != "" || requests[2].cookie != "" {
		t.Fatalf("unexpected redirected requests: %+v", requests)
	}

	for name, location := range map[string]string{
		"http scheme":         "http://github.com/Dirard/codex-lb/archive",
		"untrusted host":      "https://evil.example/archive.tar.gz",
		"nonstandard port":    "https://github.com:8443/Dirard/codex-lb/archive",
		"untrusted subdomain": "https://evil.github.com/archive.tar.gz",
		"api host":            "https://api.github.com/repos/Dirard/codex-lb/archive",
		"userinfo":            "https://synthetic-user@github.com/archive.tar.gz",
	} {
		t.Run(name, func(t *testing.T) {
			transport := &stubTransport{responses: map[string]stubResponse{
				release.ChecksumURL: {status: http.StatusOK, body: checksumBody(t, release.ArchiveName, archive)},
				release.ArchiveURL:  {status: http.StatusFound, header: map[string]string{"Location": location}, body: strings.NewReader("")},
			}}
			assertDownloadFailsClean(t, release, transport, t.TempDir(), "release redirect")
		})
	}
}

func TestReleaseRedirectChainIsBounded(t *testing.T) {
	request, err := http.NewRequest(http.MethodGet, "https://github.com/Dirard/codex-lb/releases", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkRedirect(request, make([]*http.Request, 10)); err == nil {
		t.Fatal("unbounded redirect chain accepted")
	}
}

func TestDownloadDoesNotOverwriteExistingStagedFiles(t *testing.T) {
	release := testRelease("go-v1.2.0")
	archive := buildArchive(t, archiveEntry{name: "codex-lb", typeflag: tar.TypeReg, body: []byte("new binary")})
	transport := downloadTransport(release, checksumBody(t, release.ArchiveName, archive), bytes.NewReader(archive))

	binaryStaging := t.TempDir()
	binaryPath := filepath.Join(binaryStaging, binaryName)
	if err := os.WriteFile(binaryPath, []byte("existing binary"), 0o500); err != nil {
		t.Fatal(err)
	}
	if _, err := newTestSource(transport).Download(context.Background(), release, binaryStaging); err == nil ||
		!strings.Contains(err.Error(), "staged binary creation failed") {
		t.Fatalf("expected existing-binary failure, got %v", err)
	}
	content, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "existing binary" {
		t.Fatalf("existing staged binary was modified: %q", content)
	}

	archiveStaging := t.TempDir()
	transport = downloadTransport(release, checksumBody(t, release.ArchiveName, archive), bytes.NewReader(archive))
	archivePath := filepath.Join(archiveStaging, release.ArchiveName)
	if err := os.WriteFile(archivePath, []byte("existing archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newTestSource(transport).Download(context.Background(), release, archiveStaging); err == nil ||
		!strings.Contains(err.Error(), "staged archive creation failed") {
		t.Fatalf("expected existing-archive failure, got %v", err)
	}
	content, err = os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "existing archive" {
		t.Fatalf("existing staged archive was modified: %q", content)
	}
}

func TestExtractBinaryRejectsOversizedContent(t *testing.T) {
	release := testRelease("go-v1.2.0")
	archive := buildArchive(t, archiveEntry{name: "codex-lb", typeflag: tar.TypeReg, body: bytes.Repeat([]byte("x"), 32)})
	archivePath := filepath.Join(t.TempDir(), release.ArchiveName)
	if err := os.WriteFile(archivePath, archive, 0o600); err != nil {
		t.Fatal(err)
	}
	binaryPath := filepath.Join(t.TempDir(), binaryName)
	if _, err := extractBinary(archivePath, binaryPath, 10); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected oversized-binary failure, got %v", err)
	}
	if _, err := os.Stat(binaryPath); !os.IsNotExist(err) {
		t.Fatalf("oversized binary remained staged: %v", err)
	}
}

func TestDownloadRejectsUnavailableStaging(t *testing.T) {
	release := testRelease("go-v1.2.0")
	transport := &stubTransport{responses: map[string]stubResponse{}}
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := newTestSource(transport).Download(context.Background(), release, missing); err == nil ||
		!strings.Contains(err.Error(), "staging directory unavailable") {
		t.Fatalf("expected staging failure, got %v", err)
	}
	notDirectory := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notDirectory, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newTestSource(transport).Download(context.Background(), release, notDirectory); err == nil ||
		!strings.Contains(err.Error(), "is not a directory") {
		t.Fatalf("expected staging failure, got %v", err)
	}
	if requests := transport.recorded(); len(requests) != 0 {
		t.Fatalf("staging validation reached the network: %+v", requests)
	}
}

func TestDownloadChecksumHTTPFailureStopsBeforeArchive(t *testing.T) {
	release := testRelease("go-v1.2.0")
	transport := &stubTransport{responses: map[string]stubResponse{
		release.ChecksumURL: {status: http.StatusNotFound, body: strings.NewReader("missing")},
	}}
	if _, err := newTestSource(transport).Download(context.Background(), release, t.TempDir()); err == nil ||
		!strings.Contains(err.Error(), "checksum file download returned HTTP 404") {
		t.Fatalf("expected checksum HTTP failure, got %v", err)
	}
	if requests := transport.recorded(); len(requests) != 1 {
		t.Fatalf("archive was requested after checksum failure: %+v", requests)
	}
}

func TestDownloadReturnsLowercaseHexDigest(t *testing.T) {
	// The happy-path test covers real digests; this pins the accepted
	// uppercase SHA256SUMS spelling through the full download path.
	binaryContent := []byte("uppercase checksum binary")
	archive := buildArchive(t, archiveEntry{name: "codex-lb", typeflag: tar.TypeReg, body: binaryContent})
	release := testRelease("go-v1.2.0")
	sum := sha256.Sum256(archive)
	uppercase := strings.ToUpper(hex.EncodeToString(sum[:]))
	transport := downloadTransport(release, staticChecksumBody(uppercase, release.ArchiveName), bytes.NewReader(archive))
	binary, err := newTestSource(transport).Download(context.Background(), release, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	binarySum := sha256.Sum256(binaryContent)
	if binary.SHA256 != hex.EncodeToString(binarySum[:]) {
		t.Fatalf("binary digest = %s", binary.SHA256)
	}
}
