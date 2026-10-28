package application

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDiagnosticRedactionMasksFreeTextCredentials(t *testing.T) {
	secrets := []string{
		"AKIAABCDEFGHIJKLMNOP",
		"AIzaSyA-synthetic-google-key-0123456789",
		"ghp_syntheticgithubtoken0123456789abcdef",
		"github_pat_synthetic_fine_grained_token_0123456789",
		"xoxb-synthetic-slack-token",
		"-----BEGIN PRIVATE KEY-----\nMIIsynthetic\n-----END PRIVATE KEY-----",
		"s3cr3t-url-password",
	}
	input, _ := json.Marshal(map[string]string{
		"message": "aws " + secrets[0] + " google " + secrets[1] + " gh " + secrets[2] + " pat " + secrets[3] +
			" slack " + secrets[4] + " pem " + secrets[5] + " url https://user:" + secrets[6] + "@db.example/x",
	})
	output := string(redactDiagnostic(input))
	for _, secret := range secrets {
		if strings.Contains(output, secret) {
			t.Fatalf("secret %q survived redaction: %s", secret, output)
		}
	}
	if !strings.Contains(output, "https://[redacted]@db.example/x") || !strings.Contains(output, "aws [redacted]") {
		t.Fatalf("redaction removed surrounding context: %s", output)
	}
	if plain := string(redactDiagnostic(json.RawMessage(`{"message":"Asia Pacific region is unavailable"}`))); !strings.Contains(plain, "Asia Pacific") {
		t.Fatalf("ordinary text redacted: %s", plain)
	}
}
