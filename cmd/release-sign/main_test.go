package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codex-lb/internal/adapters/releases"
)

func testSigningKey(t *testing.T) (string, ed25519.PublicKey) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), public
}

func TestSignWritesAVerifiableSignature(t *testing.T) {
	key, public := testSigningKey(t)
	sums := filepath.Join(t.TempDir(), "SHA256SUMS")
	if err := os.WriteFile(sums, []byte("abc  codex-lb-go-v1.0.0-linux-amd64.tar.gz\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	if err := run([]string{"sign", sums}, key, &bytes.Buffer{}, &stderr); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stderr.String(), "PRIVATE KEY") {
		t.Fatal("signing key printed")
	}
	content, _ := os.ReadFile(sums)
	signature, err := os.ReadFile(sums + ".sig")
	if err != nil || releases.VerifyChecksumSignature(content, signature, []ed25519.PublicKey{public}) != nil {
		t.Fatalf("signature does not verify: %v", err)
	}
	var stdout bytes.Buffer
	if err := run([]string{"public-key"}, key, &stdout, &stderr); err != nil || strings.TrimSpace(stdout.String()) != base64.StdEncoding.EncodeToString(public) {
		t.Fatalf("public key output %q: %v", stdout.String(), err)
	}
}

func TestSignWithoutKeyIsATransitionReleaseOnlyWhileNoKeyIsEmbedded(t *testing.T) {
	embedded, err := releases.TrustedReleaseKeys()
	if err != nil {
		t.Fatal(err)
	}
	sums := filepath.Join(t.TempDir(), "SHA256SUMS")
	if err := os.WriteFile(sums, []byte("abc  file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err = run([]string{"sign", sums}, "", &bytes.Buffer{}, &bytes.Buffer{})
	if len(embedded) == 0 && err != nil || len(embedded) != 0 && err == nil {
		t.Fatalf("unsigned release decision with %d embedded keys: %v", len(embedded), err)
	}
	if _, statErr := os.Stat(sums + ".sig"); !os.IsNotExist(statErr) {
		t.Fatal("signature written without a key")
	}
	if err := run([]string{"sign", sums}, "not a pem key", &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("malformed key accepted")
	}
}
