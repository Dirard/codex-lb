package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"codex-lb/internal/adapters/updatefiles"
	"codex-lb/internal/domain"
)

func TestFencedUnresponsiveCandidateIsKilledBeforePreviousStarts(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("managed workers are supported on Linux")
	}
	previous := buildManagedTestBinary(t, "go-v9.0.0")
	privateDir, err := os.MkdirTemp("", "lb-fenced-test-")
	if err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(t.TempDir(), "data")
	marker := filepath.Join(t.TempDir(), "ignoring-term")
	candidate := filepath.Join(t.TempDir(), "unresponsive-candidate")
	script := "#!/bin/sh\ntrap '' TERM\nprintf ready > " + strconv.Quote(marker) + "\nexec sleep 3600\n"
	if err := os.WriteFile(candidate, []byte(script), 0500); err != nil {
		t.Fatal(err)
	}
	host := &updateHost{
		args:    []string{"--data-dir", dataDir, "--listen", managedTestAddress(t), "--dashboard-auth-mode", "disabled"},
		dataDir: dataDir, privateDir: privateDir, parentSocket: filepath.Join(privateDir, "parent.sock"),
		token: strings.Repeat("a", 64), stdout: io.Discard, stderr: io.Discard,
		fatal: make(chan struct{}, 1), shutdownTimeout: 200 * time.Millisecond,
	}
	t.Cleanup(func() { _ = host.Shutdown(); _ = os.RemoveAll(privateDir) })
	prepareCtx, cancelPrepare := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancelPrepare()
	if err := host.PrepareStart(prepareCtx, domain.RuntimeInstallation{Path: candidate, Descriptor: domain.RuntimeDescriptor{Version: "go-v9.0.1"}}); err == nil {
		t.Fatal("unresponsive candidate unexpectedly became ready")
	}
	child := host.current()
	if child == nil {
		t.Fatal("unresponsive candidate was not retained for cleanup")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("candidate never installed its SIGTERM ignore handler")
	}
	stopCtx, cancelStop := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelStop()
	if err := host.Stop(stopCtx); err != nil || host.current() != nil {
		t.Fatalf("fenced candidate was not killed and joined: %v", err)
	}
	var exit *exec.ExitError
	status, ok := syscall.WaitStatus(0), false
	if errors.As(child.err, &exit) {
		status, ok = exit.Sys().(syscall.WaitStatus)
	}
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("fenced candidate did not require SIGKILL: %v", child.err)
	}
	descriptor, err := host.Inspect(context.Background(), previous)
	if err != nil {
		t.Fatal(err)
	}
	startCtx, cancelStart := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelStart()
	if err := host.PrepareStart(startCtx, domain.RuntimeInstallation{Path: previous, Descriptor: descriptor}); err != nil {
		t.Fatalf("previous worker could not start after candidate cleanup: %v", err)
	}
	if err := host.Stop(startCtx); err != nil {
		t.Fatalf("previous fenced worker did not stop cleanly: %v", err)
	}
}

func TestManagedBootDoesNotExecuteTamperedCurrent(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("managed workers are supported on Linux")
	}
	binary := buildManagedTestBinary(t, "go-v9.0.0")
	dataDir := filepath.Join(t.TempDir(), "data")
	store, err := updatefiles.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := (&updateHost{}).Inspect(context.Background(), binary)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.Initialize(context.Background(), binary, descriptor)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "tampered-executable-ran")
	if err := os.Chmod(state.Current.Path, 0700); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf ran > " + strconv.Quote(marker) + "\nexit 1\n"
	if err := os.WriteFile(state.Current.Path, []byte(script), 0500); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(state.Current.Path, 0500); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "serve", "--data-dir", dataDir, "--listen", managedTestAddress(t))
	command.Env = append(os.Environ(), workerSocketEnv+"=", parentSocketEnv+"=", controlTokenEnv+"=")
	var stderr bytes.Buffer
	command.Stdout, command.Stderr = io.Discard, &stderr
	if err := command.Run(); err == nil {
		t.Fatal("tampered installation unexpectedly booted")
	}
	if !strings.Contains(stderr.String(), "managed runtime startup failed; no compatible previous version") {
		t.Fatal("boot failed before reaching managed installation verification")
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("tampered current executable ran before digest verification")
	}
}
