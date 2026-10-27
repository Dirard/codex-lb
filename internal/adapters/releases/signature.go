package releases

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
)

// trustedReleaseKeys lists the base64-encoded raw ed25519 public keys whose
// signatures over SHA256SUMS this build accepts. Rotate by releasing a build
// that lists both the old and the new key, sign later releases with the new
// key, and drop the old key in a subsequent release.
//
// The list is intentionally empty until the release signing key is created.
// While it is empty, updates are verified by SHA-256 only (transition mode);
// once it contains a key, every update must carry a valid SHA256SUMS.sig.
var trustedReleaseKeys = []string{}

const (
	signatureName  = checksumName + ".sig"
	signatureLimit = 4 << 10
)

// ErrSignature reports a missing, malformed or untrusted release signature.
var ErrSignature = errors.New("release signature verification failed")

// TrustedReleaseKeys returns the public keys embedded in this build.
func TrustedReleaseKeys() ([]ed25519.PublicKey, error) {
	return parsePublicKeys(trustedReleaseKeys)
}

func parsePublicKeys(encoded []string) ([]ed25519.PublicKey, error) {
	keys := make([]ed25519.PublicKey, 0, len(encoded))
	for _, value := range encoded {
		raw, err := base64.StdEncoding.DecodeString(value)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("embedded release public key is not a base64 ed25519 key")
		}
		keys = append(keys, ed25519.PublicKey(raw))
	}
	return keys, nil
}

// EncodeSignature renders a signature file: base64 of the 64-byte signature.
func EncodeSignature(signature []byte) []byte {
	return []byte(base64.StdEncoding.EncodeToString(signature) + "\n")
}

// VerifyChecksumSignature accepts sums only when signatureFile holds an
// ed25519 signature over the exact sums bytes by one of keys.
func VerifyChecksumSignature(sums, signatureFile []byte, keys []ed25519.PublicKey) error {
	if len(keys) == 0 {
		return fmt.Errorf("%w: no trusted release key", ErrSignature)
	}
	signature, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(signatureFile)))
	if err != nil || len(signature) != ed25519.SignatureSize {
		return fmt.Errorf("%w: malformed %s", ErrSignature, signatureName)
	}
	for _, key := range keys {
		if ed25519.Verify(key, sums, signature) {
			return nil
		}
	}
	return fmt.Errorf("%w: %s is not signed by a trusted release key", ErrSignature, checksumName)
}
