package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
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
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"codex-lb/internal/adapters/updatefiles"
	"codex-lb/internal/domain"
)

func buildLegacyManagedTestBinary(t *testing.T, version string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "codex-lb")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "build", "-ldflags", "-X main.version="+version+" -X main.handoffSupport=off", "-o", path, ".")
	command.Env = append(os.Environ(), "GOPROXY=off", "GOSUMDB=off")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v: %s", version, err, output)
	}
	return path
}

// trafficProbe opens a new connection for every request so a refused or reset
// connection during the switch is counted, not hidden by keep-alive reuse.
type trafficProbe struct {
	stop      chan struct{}
	done      sync.WaitGroup
	total     atomic.Int64
	failures  atomic.Int64
	firstFail atomic.Value
}

func startTrafficProbe(address string) *trafficProbe {
	probe := &trafficProbe{stop: make(chan struct{})}
	client := managedHTTPClient()
	for range 4 {
		probe.done.Add(1)
		go func() {
			defer probe.done.Done()
			for {
				select {
				case <-probe.stop:
					return
				default:
				}
				response, err := client.Get("http://" + address + "/health/ready")
				probe.total.Add(1)
				if err == nil {
					_, _ = io.Copy(io.Discard, response.Body)
					response.Body.Close()
					if response.StatusCode != http.StatusOK {
						err = fmt.Errorf("status %d", response.StatusCode)
					}
				}
				if err != nil {
					probe.failures.Add(1)
					probe.firstFail.CompareAndSwap(nil, err.Error())
				}
				time.Sleep(2 * time.Millisecond)
			}
		}()
	}
	return probe
}

func (p *trafficProbe) finish(t *testing.T) {
	t.Helper()
	close(p.stop)
	p.done.Wait()
	if p.total.Load() < 50 {
		t.Fatalf("traffic probe sent only %d requests", p.total.Load())
	}
	if failures := p.failures.Load(); failures != 0 {
		t.Fatalf("%d of %d requests failed during the switch; first: %v", failures, p.total.Load(), p.firstFail.Load())
	}
}

// slowRequest holds a request inside a worker by sending its body in parts.
type slowRequest struct {
	conn net.Conn
	rest string
}

func startSlowRequest(t *testing.T, address string) *slowRequest {
	t.Helper()
	body := `{"name":"created across a seamless switch"}`
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	head := fmt.Sprintf("POST /api/api-keys HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n", address, len(body))
	if _, err := io.WriteString(conn, head+body[:10]); err != nil {
		t.Fatal(err)
	}
	return &slowRequest{conn: conn, rest: body[10:]}
}

func (r *slowRequest) complete(t *testing.T) {
	t.Helper()
	if _, err := io.WriteString(r.conn, r.rest); err != nil {
		t.Fatalf("in-flight request lost its connection: %v", err)
	}
	_ = r.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	response, err := http.ReadResponse(bufio.NewReader(r.conn), nil)
	if err != nil {
		t.Fatalf("in-flight request got no response: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("in-flight request finished with %d", response.StatusCode)
	}
}

func retiringWorkers(host *updateHost) int {
	host.mu.Lock()
	defer host.mu.Unlock()
	return len(host.retiring)
}

func TestManagedSeamlessSwitchKeepsServing(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("managed workers are supported on Linux")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	oldBinary := buildManagedTestBinary(t, "go-v9.0.0")
	newBinary := buildManagedTestBinary(t, "go-v9.0.1")
	dataDir, address := filepath.Join(t.TempDir(), "data"), managedTestAddress(t)
	source := &integrationReleaseSource{}
	source.set("go-v9.0.1", newBinary)
	runtime := startManagedIntegrationRuntime(t, ctx, dataDir, address, oldBinary, source)
	if status, err := runtime.service.Status(ctx); err != nil || !status.Seamless {
		t.Fatalf("handoff-capable installation does not report seamless switching: %+v", status)
	}
	probe := startTrafficProbe(address)
	inflight := startSlowRequest(t, address)
	// Accepted by the previous worker before the switch, its request is sent
	// only afterwards: retirement must answer it, not drop the connection.
	accepted, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { accepted.Close() })
	time.Sleep(200 * time.Millisecond)

	managedAction(t, address, "/api/runtime/updates/check", "")
	waitManagedLatest(t, runtime.service, "go-v9.0.1")
	managedAction(t, address, "/api/runtime/updates/apply", "go-v9.0.1")
	waitManagedPhase(t, runtime.service, "succeeded", "go-v9.0.1")
	if retiringWorkers(runtime.host) != 1 {
		t.Fatal("the previous worker did not keep running for its in-flight request")
	}
	// The new worker answers while the previous one still holds the request.
	time.Sleep(300 * time.Millisecond)
	if _, err := io.WriteString(accepted, "GET /health/ready HTTP/1.1\r\nHost: "+address+"\r\n\r\n"); err != nil {
		t.Fatalf("an accepted connection was closed by the switch: %v", err)
	}
	_ = accepted.SetReadDeadline(time.Now().Add(10 * time.Second))
	answer, err := http.ReadResponse(bufio.NewReader(accepted), nil)
	if err != nil {
		t.Fatalf("a request on a connection accepted before the switch got no answer: %v", err)
	}
	answer.Body.Close()
	if answer.StatusCode != http.StatusOK {
		t.Fatalf("a request on a connection accepted before the switch got %d", answer.StatusCode)
	}
	inflight.complete(t)
	deadline := time.Now().Add(15 * time.Second)
	for retiringWorkers(runtime.host) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the retired worker did not exit after its request completed")
		}
		time.Sleep(20 * time.Millisecond)
	}
	probe.finish(t)
	if !runtime.host.Overlap() {
		t.Fatal("the new worker cannot take the next seamless switch")
	}
	if state, err := runtime.store.Load(ctx); err != nil || state.Current.Descriptor.Version != "go-v9.0.1" || state.Previous == nil || state.Previous.Descriptor.Version != "go-v9.0.0" {
		t.Fatal("seamless switch did not commit current/previous")
	}

	// Rolling back is a seamless switch too and keeps the new data.
	probe = startTrafficProbe(address)
	managedAction(t, address, "/api/runtime/updates/rollback", "go-v9.0.0")
	waitManagedPhase(t, runtime.service, "succeeded", "go-v9.0.0")
	probe.finish(t)
	runtime.close()
	data, err := openData(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer data.close()
	keys, err := data.store.ListAPIKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, key := range keys {
		found = found || key.Name == "created across a seamless switch"
	}
	if !found {
		t.Fatal("the request served across the switch was not persisted")
	}
}

func TestManagedSwitchesBetweenLegacyAndHandoffWorkers(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("managed workers are supported on Linux")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	legacy := buildLegacyManagedTestBinary(t, "go-v9.0.0")
	modern := buildManagedTestBinary(t, "go-v9.0.1")
	dataDir, address := filepath.Join(t.TempDir(), "data"), managedTestAddress(t)
	source := &integrationReleaseSource{}
	source.set("go-v9.0.1", modern)
	runtime := startManagedIntegrationRuntime(t, ctx, dataDir, address, legacy, source)
	if runtime.host.Overlap() || runtime.host.ownsLock() {
		t.Fatal("a legacy worker must bind and lock by itself")
	}
	managedAction(t, address, "/api/runtime/updates/check", "")
	waitManagedLatest(t, runtime.service, "go-v9.0.1")
	managedAction(t, address, "/api/runtime/updates/apply", "go-v9.0.1")
	waitManagedPhase(t, runtime.service, "succeeded", "go-v9.0.1")
	waitManagedHTTPVersion(t, address, "go-v9.0.1")
	if !runtime.host.Overlap() {
		t.Fatal("after a classic switch to a handoff worker the launcher must own the listener")
	}
	managedAction(t, address, "/api/runtime/updates/rollback", "go-v9.0.0")
	waitManagedPhase(t, runtime.service, "succeeded", "go-v9.0.0")
	waitManagedHTTPVersion(t, address, "go-v9.0.0")
	if runtime.host.Overlap() || runtime.host.ownsLock() {
		t.Fatal("the launcher kept the listener or lock while a legacy worker serves")
	}
}

func TestBootstrapDelegatesToNewerInstalledLauncher(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("managed workers are supported on Linux")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	bootstrap := buildManagedTestBinary(t, "go-v9.0.0")
	newer := buildManagedTestBinary(t, "go-v9.0.1")
	dataDir, address := filepath.Join(t.TempDir(), "data"), managedTestAddress(t)
	store, err := updatefiles.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	host := &updateHost{}
	descriptor, err := host.Inspect(ctx, bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.Initialize(ctx, bootstrap, descriptor)
	if err != nil {
		t.Fatal(err)
	}
	newerDescriptor, err := host.Inspect(ctx, newer)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(newer)
	if err != nil {
		t.Fatal(err)
	}
	stage, err := store.Stage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(stage, "codex-lb")
	if err := os.WriteFile(staged, content, 0o500); err != nil {
		t.Fatal(err)
	}
	installed, err := store.Install(ctx, domain.RuntimeBinary{Path: staged, SHA256: fmt.Sprintf("%x", sha256.Sum256(content))}, newerDescriptor)
	store.DiscardStage(stage)
	if err != nil {
		t.Fatal(err)
	}
	previous := state.Current
	state.Current, state.Previous, state.Phase = installed, &previous, "succeeded"
	if err := store.Save(ctx, state); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	launch := func() (*exec.Cmd, *bytes.Buffer) {
		command := exec.Command(bootstrap, "serve", "--data-dir", dataDir, "--listen", address, "--dashboard-auth-mode", "disabled", "--shutdown-grace", "1s")
		// Release discovery must not leave the host during the test.
		command.Env = append(os.Environ(), "HTTPS_PROXY=http://127.0.0.1:9", "HTTP_PROXY=http://127.0.0.1:9", "NO_PROXY=")
		var output bytes.Buffer
		command.Stdout, command.Stderr = &output, &output
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		return command, &output
	}
	stop := func(command *exec.Cmd) {
		_ = command.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = command.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			_ = command.Process.Kill()
			<-done
		}
	}
	command, output := launch()
	waitManagedHTTPVersion(t, address, "go-v9.0.1")
	executable, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", command.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	if executable != installed.Path {
		t.Fatalf("launcher runs %s, want the installed newer version %s\n%s", executable, installed.Path, output)
	}
	// The worker serves before its launcher finished activation and recorded
	// the confirmation; stopping earlier would test an interrupted start.
	var delegation updatefiles.LauncherDelegation
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		data, err := os.ReadFile(filepath.Join(dataDir, "updates", "launcher.json"))
		if err == nil && json.Unmarshal(data, &delegation) == nil && delegation.Confirmed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("delegation was not confirmed: %+v, %v\n%s", delegation, err, output)
		}
	}
	stop(command)
	if delegation.SHA256 != installed.SHA256 {
		t.Fatalf("confirmed delegation names %s, want %s", delegation.SHA256, installed.SHA256)
	}

	// Delegations that repeatedly ended unconfirmed are not retried: the
	// bootstrap serves itself.
	reopened, err := updatefiles.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.SaveDelegation(updatefiles.LauncherDelegation{SHA256: installed.SHA256, Attempts: delegationAttempts}); err != nil {
		t.Fatal(err)
	}
	reopened.Close()
	command, output = launch()
	waitManagedHTTPVersion(t, address, "go-v9.0.1")
	executable, err = os.Readlink(fmt.Sprintf("/proc/%d/exe", command.Process.Pid))
	if err != nil || executable != bootstrap {
		t.Fatalf("launcher after a failed delegation runs %s, want the bootstrap", executable)
	}
	stop(command)
	if !strings.Contains(output.String(), "did not start; using the bootstrap launcher") {
		t.Fatalf("failed delegation was not reported:\n%s", output)
	}
}
