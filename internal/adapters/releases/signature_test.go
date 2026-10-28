package releases

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"testing"
)

func TestEmbeddedReleaseKeysParse(t *testing.T) {
	if _, err := TrustedReleaseKeys(); err != nil {
		t.Fatalf("embedded trusted release keys are malformed: %v", err)
	}
	if _, err := parsePublicKeys([]string{"not-a-key"}); err == nil {
		t.Fatal("malformed key accepted")
	}
}

func TestDownloadRequiresTrustedSignatureWhenKeysAreEmbedded(t *testing.T) {
	trustedPublic, trustedPrivate, _ := ed25519.GenerateKey(rand.Reader)
	_, foreignPrivate, _ := ed25519.GenerateKey(rand.Reader)
	archive := buildArchive(t, archiveEntry{name: "codex-lb", typeflag: tar.TypeReg, body: []byte("signed binary")})
	release := testRelease("go-v1.3.0")
	digest := sha256.Sum256(archive)
	sums := []byte(hex.EncodeToString(digest[:]) + "  " + release.ArchiveName + "\n")
	tampered := []byte(hex.EncodeToString(make([]byte, 32)) + "  " + release.ArchiveName + "\n")
	for _, tc := range []struct {
		name      string
		sums      []byte
		signature []byte
		missing   bool
		ok        bool
	}{
		{"valid", sums, EncodeSignature(ed25519.Sign(trustedPrivate, sums)), false, true},
		{"missing signature", sums, nil, true, false},
		{"foreign key", sums, EncodeSignature(ed25519.Sign(foreignPrivate, sums)), false, false},
		{"modified checksums", tampered, EncodeSignature(ed25519.Sign(trustedPrivate, sums)), false, false},
		{"malformed signature", sums, []byte("not base64"), false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := downloadTransport(release, bytes.NewReader(tc.sums), bytes.NewReader(archive))
			signature := stubResponse{status: http.StatusOK, body: bytes.NewReader(tc.signature)}
			if tc.missing {
				signature = stubResponse{status: http.StatusNotFound, body: bytes.NewReader(nil)}
			}
			transport.responses[release.ChecksumURL+".sig"] = signature
			source := newTestSource(transport)
			source.keys = []ed25519.PublicKey{trustedPublic}
			staging := t.TempDir()
			binary, err := source.Download(context.Background(), release, staging)
			if tc.ok {
				if err != nil || binary.Path == "" {
					t.Fatalf("signed release refused: %v", err)
				}
				return
			}
			if !errors.Is(err, ErrSignature) {
				t.Fatalf("unsigned or untrusted release accepted: %v", err)
			}
			entries, _ := os.ReadDir(staging)
			if len(entries) != 0 {
				t.Fatal("refused release left staged files")
			}
			for _, request := range transport.requests {
				if request.url == release.ArchiveURL {
					t.Fatal("archive downloaded before the signature was verified")
				}
			}
		})
	}
}

func TestDownloadWithoutEmbeddedKeysKeepsChecksumOnlyTransition(t *testing.T) {
	archive := buildArchive(t, archiveEntry{name: "codex-lb", typeflag: tar.TypeReg, body: []byte("transition binary")})
	release := testRelease("go-v1.3.1")
	transport := downloadTransport(release, checksumBody(t, release.ArchiveName, archive), bytes.NewReader(archive))
	source := newTestSource(transport)
	source.keys = nil
	if _, err := source.Download(context.Background(), release, t.TempDir()); err != nil {
		t.Fatalf("transition release refused: %v", err)
	}
	for _, request := range transport.requests {
		if request.url == release.ChecksumURL+".sig" {
			t.Fatal("signature fetched although no trusted key is embedded")
		}
	}
}
