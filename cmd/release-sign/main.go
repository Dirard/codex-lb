// Command release-sign signs and verifies codex-lb release checksum files.
//
//	release-sign sign <SHA256SUMS>    writes <SHA256SUMS>.sig using RELEASE_SIGNING_KEY
//	release-sign verify <SHA256SUMS>  checks <SHA256SUMS>.sig against the embedded keys
//	release-sign public-key           prints the base64 public key of RELEASE_SIGNING_KEY
//
// RELEASE_SIGNING_KEY holds a PKCS#8 PEM ed25519 private key, for example from
// `openssl genpkey -algorithm ed25519`. The key value is never printed.
package main

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"

	"codex-lb/internal/adapters/releases"
)

const keyVariable = "RELEASE_SIGNING_KEY"

func main() {
	if err := run(os.Args[1:], os.Getenv(keyVariable), os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "release-sign:", err)
		os.Exit(1)
	}
}

func run(args []string, encodedKey string, stdout, stderr io.Writer) error {
	embedded, err := releases.TrustedReleaseKeys()
	if err != nil {
		return err
	}
	switch {
	case len(args) == 2 && args[0] == "sign":
		return sign(args[1], encodedKey, embedded, stderr)
	case len(args) == 2 && args[0] == "verify":
		return verify(args[1], embedded)
	case len(args) == 1 && args[0] == "public-key":
		key, err := privateKey(encodedKey)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(stdout, base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey)))
		return err
	default:
		return errors.New("usage: release-sign sign|verify <SHA256SUMS> | release-sign public-key")
	}
}

// sign writes the signature next to sums. Without a key it succeeds only while
// no trusted key is embedded (unsigned transition releases).
func sign(sumsPath, encodedKey string, embedded []ed25519.PublicKey, stderr io.Writer) error {
	if encodedKey == "" {
		if len(embedded) != 0 {
			return fmt.Errorf("%s is required: this build embeds trusted release keys", keyVariable)
		}
		_, err := fmt.Fprintf(stderr, "release-sign: %s is not set; publishing an unsigned release (no trusted key is embedded yet)\n", keyVariable)
		return err
	}
	key, err := privateKey(encodedKey)
	if err != nil {
		return err
	}
	sums, err := os.ReadFile(sumsPath)
	if err != nil {
		return err
	}
	signature := releases.EncodeSignature(ed25519.Sign(key, sums))
	if len(embedded) != 0 {
		if err := releases.VerifyChecksumSignature(sums, signature, embedded); err != nil {
			return fmt.Errorf("%s does not match a trusted key embedded in this build: %w", keyVariable, err)
		}
	} else if _, err := fmt.Fprintln(stderr, "release-sign: no trusted key is embedded yet; updaters will not verify this signature"); err != nil {
		return err
	}
	return os.WriteFile(sumsPath+".sig", signature, 0o644)
}

func verify(sumsPath string, embedded []ed25519.PublicKey) error {
	sums, err := os.ReadFile(sumsPath)
	if err != nil {
		return err
	}
	signature, err := os.ReadFile(sumsPath + ".sig")
	if err != nil {
		return err
	}
	return releases.VerifyChecksumSignature(sums, signature, embedded)
}

func privateKey(encoded string) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode([]byte(encoded))
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, fmt.Errorf("%s must be a PKCS#8 PEM private key", keyVariable)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%s is not a valid PKCS#8 key", keyVariable)
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%s must be an ed25519 key", keyVariable)
	}
	return key, nil
}
