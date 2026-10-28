package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime/debug"
	"slices"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

func TestParseMemoryLimit(t *testing.T) {
	for input, want := range map[string]int64{"auto": 0, "": 0, "off": -1, "OFF": -1, "3GiB": 3 << 30, "768MiB": 768 << 20, "1073741824": 1 << 30, "1048576KiB": 1 << 30} {
		got, err := parseMemoryLimit(input)
		if err != nil || got != want {
			t.Fatalf("parseMemoryLimit(%q) = %d, %v; want %d", input, got, err, want)
		}
	}
	for _, input := range []string{"10MiB", "-1GiB", "3GB", "lots", "0"} {
		if _, err := parseMemoryLimit(input); err == nil {
			t.Fatalf("parseMemoryLimit(%q) accepted", input)
		}
	}
}

func TestServeCapacityFlags(t *testing.T) {
	cfg, err := parseConfig("serve", []string{"--data-dir", t.TempDir(), "--memory-limit", "2GiB", "--max-generations", "300", "--max-client-websockets", "1000"}, io.Discard)
	if err != nil || cfg.memoryLimit != 2<<30 || cfg.maxGenerations != 300 || cfg.maxClientWebSockets != 1000 {
		t.Fatalf("cfg = %+v err %v", cfg, err)
	}
	if _, err := parseConfig("serve", []string{"--data-dir", t.TempDir(), "--max-generations", "0"}, io.Discard); err == nil {
		t.Fatal("zero max-generations accepted")
	}
	body, err := parseConfig("serve", []string{"--data-dir", t.TempDir(), "--body-memory-budget", "512MiB", "--body-memory-key-share", "25%"}, io.Discard)
	if err != nil || body.bodyMemoryBudget != 512<<20 || body.bodyMemoryKeyShare != 25 {
		t.Fatalf("body flags = %d/%d err %v", body.bodyMemoryBudget, body.bodyMemoryKeyShare, err)
	}
	plan := planCapacity(body, domain.HostResources{CPUs: 6, MemoryBytes: 4 << 30})
	if plan.BodyBudgetBytes != 512<<20 || plan.BodyBudgetKeyBytes != 128<<20 || !slices.Equal(plan.Overridden, []string{"body_memory_budget", "body_memory_key_share"}) {
		t.Fatalf("body plan = %+v", plan)
	}
	for _, args := range [][]string{{"--body-memory-budget", "16MiB"}, {"--body-memory-budget", "0"}, {"--body-memory-key-share", "0"}, {"--body-memory-key-share", "101"}, {"--body-memory-key-share", "half"}} {
		if _, err := parseConfig("serve", append([]string{"--data-dir", t.TempDir()}, args...), io.Discard); err == nil {
			t.Fatalf("%v accepted", args)
		}
	}
}

// TestRuntimeRoutesShareOneBodyBudget holds a declared Responses body that has
// not arrived yet; compact, Chat Completions and transcription bodies then get
// local capacity from the same budget until the holder disconnects.
func TestRuntimeRoutesShareOneBodyBudget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cfg := testConfig(t)
	cfg.bodyMemoryBudget, cfg.bodyMemoryKeyShare = 32<<20, 100
	r, err := openRuntime(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer r.close()
	const secret = "synthetic-runtime-budget-key"
	if err := r.data.store.SaveAPIKey(ctx, domain.APIKey{ID: "budget-key", Name: "budget test", KeyHash: fmt.Sprintf("%x", sha256.Sum256([]byte(secret))), KeyPrefix: "synthetic", IsActive: true}, time.Now()); err != nil {
		t.Fatal(err)
	}
	settings, err := r.data.store.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.APIKeyAuthEnabled = true
	if err := r.data.store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(r.server.Handler)
	defer server.Close()
	// declare sends only the request head, so the server charges the declared
	// length and then waits for a body that never comes.
	declare := func(path, contentType string, length int) net.Conn {
		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", strings.TrimPrefix(server.URL, "http://"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		fmt.Fprintf(conn, "POST %s HTTP/1.1\r\nHost: codex-lb\r\nAuthorization: Bearer %s\r\nContent-Type: %s\r\nContent-Length: %d\r\n\r\n", path, secret, contentType, length)
		return conn
	}
	refused := func(conn net.Conn) bool {
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		response, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		payload, _ := io.ReadAll(response.Body)
		return response.StatusCode == http.StatusServiceUnavailable && bytes.Contains(payload, []byte("local_capacity_exceeded")) && response.Header.Get("Retry-After") == "2"
	}
	holder := declare("/v1/responses", "application/json", 20<<20)
	for deadline := time.Now().Add(5 * time.Second); !refused(declare("/backend-api/codex/responses/compact", "application/json", 20<<20)); {
		if time.Now().After(deadline) {
			t.Fatal("compact did not share the Responses body budget")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !refused(declare("/v1/chat/completions", "application/json", 20<<20)) {
		t.Fatal("Chat Completions did not share the body budget")
	}
	if !refused(declare("/backend-api/transcribe", "multipart/form-data; boundary=synthetic", 20<<20)) {
		t.Fatal("transcription did not share the body budget")
	}
	holder.Close()
	invalid := bytes.Repeat([]byte("x"), 20<<20)
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		request, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/backend-api/codex/responses/compact", bytes.NewReader(invalid))
		request.Header.Set("Authorization", "Bearer "+secret)
		request.Header.Set("Content-Type", "application/json")
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode == http.StatusBadRequest {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("disconnected holder kept its charge: compact status %d", response.StatusCode)
		}
	}
}

func TestPlanCapacityHonorsExportedGOMEMLIMIT(t *testing.T) {
	previous := debug.SetMemoryLimit(-1)
	t.Cleanup(func() { debug.SetMemoryLimit(previous) })
	debug.SetMemoryLimit(768 << 20)
	t.Setenv("GOMEMLIMIT", "768MiB")
	host := domain.HostResources{CPUs: 6, MemoryBytes: 4 << 30, MemorySource: domain.MemorySourcePhysical}
	plan := planCapacity(config{}, host)
	if plan.GoMemoryLimitBytes != 0 || plan.SizingMemorySource != domain.MemorySourceGoLimit || plan.MaxGenerations != 512 {
		t.Fatalf("plan = %+v", plan)
	}
	explicit := planCapacity(config{memoryLimit: 3 << 30}, host)
	if explicit.GoMemoryLimitBytes != 3<<30 || explicit.SizingMemorySource != domain.MemorySourceFlag {
		t.Fatalf("explicit flag must win over GOMEMLIMIT: %+v", explicit)
	}
}

func TestOpenRuntimeAppliesCapacityOverrides(t *testing.T) {
	previous := debug.SetMemoryLimit(-1)
	t.Cleanup(func() { debug.SetMemoryLimit(previous) })
	cfg := testConfig(t)
	cfg.maxGenerations, cfg.maxClientWebSockets, cfg.memoryLimit = 40, 50, 512<<20
	r, err := openRuntime(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer r.close()
	if got := r.proxy.AdmissionCapacity(); got != 40+64 {
		t.Fatalf("admission capacity = %d, want 104", got)
	}
	if got := debug.SetMemoryLimit(-1); got != 512<<20 {
		t.Fatalf("Go memory limit = %d", got)
	}
}
