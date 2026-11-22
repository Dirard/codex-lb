package main

import (
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestAntigravityClientVersionComesFromTheEnvironment(t *testing.T) {
	t.Setenv("CODEX_LB_DATA_DIR", filepath.Join(t.TempDir(), "antigravity"))
	t.Setenv("CODEX_LB_CONNECT_ADDRESS", "")
	t.Setenv("CODEX_LB_ANTIGRAVITY_CLIENT_VERSION", "")
	cfg, err := parseConfig("serve", nil, io.Discard)
	if err != nil || cfg.antigravityClientVersion != "2.34.0" {
		t.Fatalf("default version: %q %v", cfg.antigravityClientVersion, err)
	}
	t.Setenv("CODEX_LB_ANTIGRAVITY_CLIENT_VERSION", " 2.35.1 ")
	if cfg, err = parseConfig("serve", nil, io.Discard); err != nil || cfg.antigravityClientVersion != "2.35.1" {
		t.Fatalf("configured version: %q %v", cfg.antigravityClientVersion, err)
	}
	for _, value := range []string{"2.35", "latest", "2.35.1; cl=1026637980", "v2.35.1"} {
		t.Setenv("CODEX_LB_ANTIGRAVITY_CLIENT_VERSION", value)
		_, err := parseConfig("serve", nil, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "CODEX_LB_ANTIGRAVITY_CLIENT_VERSION") {
			t.Fatalf("%q accepted: %v", value, err)
		}
	}
}
