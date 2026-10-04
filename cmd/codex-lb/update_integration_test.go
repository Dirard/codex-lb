package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"codex-lb/internal/adapters/updatecontrol"
	"codex-lb/internal/adapters/updatefiles"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

type integrationReleaseSource struct {
	mu         sync.Mutex
	release    domain.RuntimeRelease
	executable string
}

func (s *integrationReleaseSource) set(version, executable string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.release = domain.RuntimeRelease{Version: version}
	s.executable = executable
}

func (s *integrationReleaseSource) Latest(context.Context) (domain.RuntimeRelease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.release, nil
}

func (s *integrationReleaseSource) Download(ctx context.Context, release domain.RuntimeRelease, dir string) (domain.RuntimeBinary, error) {
	s.mu.Lock()
	path, version := s.executable, s.release.Version
	s.mu.Unlock()
	if version != release.Version {
		return domain.RuntimeBinary{}, fmt.Errorf("fixture release changed")
	}
	input, err := os.Open(path)
	if err != nil {
		return domain.RuntimeBinary{}, err
	}
	defer input.Close()
	outputPath := filepath.Join(dir, "codex-lb")
	output, err := os.OpenFile(outputPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0500)
	if err != nil {
		return domain.RuntimeBinary{}, err
	}
	hash := sha256.New()
	_, err = io.Copy(io.MultiWriter(output, hash), input)
	if err == nil {
		err = ctx.Err()
	}
	err = errors.Join(err, output.Close())
	if err != nil {
		return domain.RuntimeBinary{}, err
	}
	return domain.RuntimeBinary{Path: outputPath, SHA256: fmt.Sprintf("%x", hash.Sum(nil))}, nil
}

type managedIntegrationRuntime struct {
	store   *updatefiles.Store
	host    *updateHost
	service *application.RuntimeUpdates
	server  *http.Server
	control net.Listener
	dir     string
	once    sync.Once
}

func (m *managedIntegrationRuntime) close() {
	m.once.Do(func() {
		if m.service != nil {
			_ = m.service.Close()
		}
		if m.host != nil {
			_ = m.host.Shutdown()
		}
		if m.server != nil {
			_ = m.server.Close()
		}
		if m.control != nil {
			_ = m.control.Close()
		}
		if m.store != nil {
			_ = m.store.Close()
		}
		if m.dir != "" {
			_ = os.RemoveAll(m.dir)
		}
	})
}

func buildManagedTestBinary(t *testing.T, version string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "codex-lb")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "build", "-ldflags", "-X main.version="+version, "-o", path, ".")
	command.Env = append(os.Environ(), "GOPROXY=off", "GOSUMDB=off")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v: %s", version, err, output)
	}
	return path
}

func managedTestAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func startManagedIntegrationRuntime(t *testing.T, ctx context.Context, dataDir, address, bootstrap string, source *integrationReleaseSource) *managedIntegrationRuntime {
	t.Helper()
	m := &managedIntegrationRuntime{}
	t.Cleanup(m.close)
	var err error
	m.store, err = updatefiles.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := (&updateHost{}).Inspect(ctx, bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	state, err := m.store.Initialize(ctx, bootstrap, descriptor)
	if err != nil {
		t.Fatal(err)
	}
	m.dir, err = os.MkdirTemp("", "lb-update-test-")
	if err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("a", 64)
	parentSocket := filepath.Join(m.dir, "parent.sock")
	m.control, err = net.Listen("unix", parentSocket)
	if err != nil {
		t.Fatal(err)
	}
	m.host = &updateHost{args: []string{"--data-dir", dataDir, "--listen", address, "--dashboard-auth-mode", "disabled", "--shutdown-grace", "1s"}, dataDir: dataDir, privateDir: m.dir, parentSocket: parentSocket, token: token, stdout: io.Discard, stderr: io.Discard, fatal: make(chan struct{}, 1)}
	m.service = application.NewRuntimeUpdates(ctx, source, m.store, m.host, state, application.RuntimeUpdateConfig{WaitTimeout: 5 * time.Second})
	m.server = &http.Server{Handler: updatecontrol.Protect(token, updatecontrol.Handler(m.service))}
	go func() { _ = m.server.Serve(m.control) }()
	if err := prepareManagedBoot(ctx, m.store, m.host, &state); err != nil {
		t.Fatal(err)
	}
	fence, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	request, err := http.NewRequestWithContext(fence, http.MethodGet, "http://"+address+"/health/ready", nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	response, err := managedHTTPClient().Do(request)
	cancel()
	if err == nil {
		response.Body.Close()
		t.Fatal("worker served public traffic before activation")
	}
	if err := m.host.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	waitManagedHTTPVersion(t, address, state.Current.Descriptor.Version)
	return m
}

func managedHTTPClient() *http.Client {
	return &http.Client{Timeout: time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
}

func managedStatus(address string) (domain.RuntimeUpdateStatus, error) {
	response, err := managedHTTPClient().Get("http://" + address + "/api/runtime/updates")
	if err != nil {
		return domain.RuntimeUpdateStatus{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return domain.RuntimeUpdateStatus{}, fmt.Errorf("status %d", response.StatusCode)
	}
	var status domain.RuntimeUpdateStatus
	return status, json.NewDecoder(response.Body).Decode(&status)
}

func waitManagedHTTPVersion(t *testing.T, address, version string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		status, err := managedStatus(address)
		if err == nil && status.CurrentVersion == version {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("public runtime did not serve version %s", version)
}

func managedAction(t *testing.T, address, path, version string) {
	t.Helper()
	var body io.Reader = http.NoBody
	if version != "" {
		body = strings.NewReader(fmt.Sprintf(`{"version":%q}`, version))
	}
	request, err := http.NewRequest(http.MethodPost, "http://"+address+path, body)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := managedHTTPClient().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("%s status %d", path, response.StatusCode)
	}
}

func waitManagedPhase(t *testing.T, service *application.RuntimeUpdates, phase, version string) domain.RuntimeUpdateStatus {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		status, err := service.Status(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if status.Phase == phase && status.CurrentVersion == version {
			return status
		}
		if status.Phase == "failed" && phase != "failed" {
			t.Fatalf("runtime update failed: %s", status.LastError)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("runtime update did not reach %s at %s", phase, version)
	return domain.RuntimeUpdateStatus{}
}

func verifyManagedBackup(t *testing.T, dataDir, keyHash string) {
	t.Helper()
	root := filepath.Join(dataDir, "updates")
	pointer, err := os.ReadFile(filepath.Join(root, "backup-current"))
	if err != nil || !strings.HasPrefix(string(pointer), "backup-") || filepath.Base(string(pointer)) != string(pointer) {
		t.Fatal("consistent backup marker missing")
	}
	backup := filepath.Join(root, string(pointer))
	key, err := os.ReadFile(filepath.Join(dataDir, "encryption.key"))
	if err != nil {
		t.Fatal(err)
	}
	backupKey, err := os.ReadFile(filepath.Join(backup, "encryption.key"))
	if err != nil || sha256.Sum256(key) != sha256.Sum256(backupKey) {
		t.Fatal("backup encryption key differs")
	}
	database, err := sql.Open("sqlite", "file:"+filepath.Join(backup, "codex-lb.sqlite3")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var storedHash string
	if err := database.QueryRow("SELECT key_hash FROM api_keys WHERE id='update-integration-key'").Scan(&storedHash); err != nil || storedHash != keyHash {
		t.Fatal("backup database lost the seeded API key")
	}
}

func failedManagedCandidate(t *testing.T, descriptor domain.RuntimeDescriptor) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fails-to-serve")
	descriptor.Version = "go-v9.0.2"
	encoded, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nif [ \"$1\" = update-info ]; then\n  printf '%s\\n' '" + string(encoded) + "'\n  exit 0\nfi\nexit 37\n"
	if err := os.WriteFile(path, []byte(script), 0500); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertPrivateControlDenied(t *testing.T, socket, path string) {
	t.Helper()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	response, err := client.Get("http://runtime" + path)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("private control without token returned %d", response.StatusCode)
	}
}

func TestManagedBinaryUpdateRollbackAndFailedCandidate(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("managed worker process is supported on Linux")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	oldBinary := buildManagedTestBinary(t, "go-v9.0.0")
	newBinary := buildManagedTestBinary(t, "go-v9.0.1")
	dataDir, address := filepath.Join(t.TempDir(), "data"), managedTestAddress(t)
	data, err := openData(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte("synthetic-update-key")))
	key := domain.APIKey{ID: "update-integration-key", Name: "update integration", KeyHash: hash, KeyPrefix: "synthetic", IsActive: true, CreatedAt: time.Now()}
	if err := data.store.SaveAPIKey(ctx, key, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := data.close(); err != nil {
		t.Fatal(err)
	}
	source := &integrationReleaseSource{}
	source.set("go-v9.0.1", newBinary)
	first := startManagedIntegrationRuntime(t, ctx, dataDir, address, oldBinary, source)
	assertPrivateControlDenied(t, first.host.parentSocket, "/status")
	assertPrivateControlDenied(t, filepath.Join(first.host.privateDir, "worker.sock"), "/ready")
	if err := first.host.current().control.Call(ctx, http.MethodPost, "/prepare-stop", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := managedStatus(address); err == nil {
		t.Fatal("prepared idle gate still served public work")
	}
	if err := first.host.current().control.Call(ctx, http.MethodPost, "/cancel-stop", nil, nil); err != nil {
		t.Fatal(err)
	}
	waitManagedHTTPVersion(t, address, "go-v9.0.0")
	managedAction(t, address, "/api/runtime/updates/check", "")
	waitManagedLatest(t, first.service, "go-v9.0.1")
	managedAction(t, address, "/api/runtime/updates/apply", "go-v9.0.1")
	waitManagedPhase(t, first.service, "succeeded", "go-v9.0.1")
	waitManagedHTTPVersion(t, address, "go-v9.0.1")
	verifyManagedBackup(t, dataDir, hash)
	request, err := http.NewRequest(http.MethodPost, "http://"+address+"/api/api-keys", strings.NewReader(`{"name":"created after upgrade"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := managedHTTPClient().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var postUpgradeKey struct {
		ID string `json:"id"`
	}
	decodeErr := json.NewDecoder(response.Body).Decode(&postUpgradeKey)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || decodeErr != nil || postUpgradeKey.ID == "" {
		t.Fatal("could not create API key after upgrade")
	}
	first.close()

	second := startManagedIntegrationRuntime(t, ctx, dataDir, address, oldBinary, source)
	if state, err := second.store.Load(ctx); err != nil || state.Current.Descriptor.Version != "go-v9.0.1" || state.Previous == nil || state.Previous.Descriptor.Version != "go-v9.0.0" {
		t.Fatal("restart ignored the committed current/previous metadata")
	}
	managedAction(t, address, "/api/runtime/updates/rollback", "go-v9.0.0")
	waitManagedPhase(t, second.service, "succeeded", "go-v9.0.0")
	waitManagedHTTPVersion(t, address, "go-v9.0.0")
	verifyManagedBackup(t, dataDir, hash)

	descriptor, err := second.host.Inspect(ctx, oldBinary)
	if err != nil {
		t.Fatal(err)
	}
	source.set("go-v9.0.2", failedManagedCandidate(t, descriptor))
	managedAction(t, address, "/api/runtime/updates/check", "")
	waitManagedLatest(t, second.service, "go-v9.0.2")
	managedAction(t, address, "/api/runtime/updates/apply", "go-v9.0.2")
	status := waitManagedPhase(t, second.service, "failed", "go-v9.0.0")
	if !strings.Contains(status.LastError, "previous version restored") {
		t.Fatalf("failed startup was not recovered: %s", status.LastError)
	}
	waitManagedHTTPVersion(t, address, "go-v9.0.0")
	if state, err := second.store.Load(ctx); err != nil || state.Pending != nil || state.Current.Descriptor.Version != "go-v9.0.0" {
		t.Fatal("failed candidate was committed")
	}
	second.close()
	data, err = openData(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer data.close()
	stored, err := data.store.GetAPIKey(ctx, key.ID)
	if err != nil || stored.KeyHash != key.KeyHash {
		t.Fatal("live database lost the seeded API key")
	}
	postUpgrade, err := data.store.GetAPIKey(ctx, postUpgradeKey.ID)
	if err != nil || postUpgrade.Name != "created after upgrade" {
		t.Fatal("rollback restored the pre-update database instead of preserving new data")
	}
}

func waitManagedLatest(t *testing.T, service *application.RuntimeUpdates, version string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, err := service.Status(context.Background())
		if err == nil && status.LatestVersion == version && status.Phase == "idle" {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("release check did not publish %s", version)
}
