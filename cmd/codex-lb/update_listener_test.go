package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"codex-lb/internal/adapters/updatecontrol"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
	"golang.org/x/sys/unix"
)

func listenerNonblocking(t *testing.T, file *os.File) bool {
	t.Helper()
	raw, err := file.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var flags int
	var flagErr error
	if err := raw.Control(func(fd uintptr) { flags, flagErr = unix.FcntlInt(fd, unix.F_GETFL, 0) }); err != nil || flagErr != nil {
		t.Fatal(err, flagErr)
	}
	return flags&unix.O_NONBLOCK != 0
}

// Starting a worker must not turn the shared listening socket blocking: a
// serving worker's next accept would then hang in the kernel.
func TestSpawnKeepsTheSharedListenerNonblocking(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("managed workers are supported on Linux")
	}
	dataDir := filepath.Join(t.TempDir(), "data")
	privateDir, err := os.MkdirTemp("", "lb-listener-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(privateDir) })
	host := &updateHost{dataDir: dataDir, listen: managedTestAddress(t), privateDir: privateDir, parentSocket: filepath.Join(privateDir, "parent.sock"),
		token: strings.Repeat("d", 64), stdout: os.Stderr, stderr: os.Stderr, fatal: make(chan struct{}, 1)}
	if err := host.own(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.release)
	if !listenerNonblocking(t, host.listener) {
		t.Fatal("owned listener is blocking before any spawn")
	}
	child, err := host.spawn(domain.RuntimeInstallation{Path: "/bin/true", Descriptor: domain.RuntimeDescriptor{Version: "go-v9.0.0", Handoff: domain.RuntimeHandoffProtocol}}, false)
	if err != nil {
		t.Fatal(err)
	}
	<-child.done
	if !listenerNonblocking(t, host.listener) {
		t.Fatal("spawning a worker cleared O_NONBLOCK on the shared listener")
	}
}

// A realistic peer load is far larger than the update API's 4 KiB bodies.
func TestWorkerControlAcceptsALargePeerLoad(t *testing.T) {
	t.Parallel()
	load := application.WorkerLoad{Active: 400, Queued: 100, Accounts: map[string]application.AccountLoad{}}
	for i := range 200 {
		keys := map[string]int{}
		for k := range 5 {
			keys[fmt.Sprintf("key-%03d-%d", i, k)] = 2
		}
		load.Accounts[fmt.Sprintf("account-%03d-0123456789abcdef", i)] = application.AccountLoad{Creates: 3, Streams: 10, Keys: keys}
	}
	var received application.WorkerLoad
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /peer-load", func(w http.ResponseWriter, r *http.Request) {
		var report workerLoadReport
		if err := jsonDecode(r, &report); err != nil {
			http.Error(w, "Invalid load", http.StatusBadRequest)
			return
		}
		received = report.Load
		w.WriteHeader(http.StatusNoContent)
	})
	token := strings.Repeat("f", 64)
	server := httptest.NewServer(updatecontrol.ProtectWithLimit(token, updatecontrol.MaxResponseBytes, mux))
	defer server.Close()
	body, err := jsonBody(workerLoadReport{Load: load, Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(body) <= 4096 {
		t.Fatalf("fixture is too small to prove the limit: %d bytes", len(body))
	}
	request, err := http.NewRequest(http.MethodPut, server.URL+"/peer-load", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent || len(received.Accounts) != 200 {
		t.Fatalf("large peer load refused: %d, %d accounts", response.StatusCode, len(received.Accounts))
	}
}

func jsonBody(value any) ([]byte, error) { return json.Marshal(value) }

func jsonDecode(r *http.Request, value any) error { return json.NewDecoder(r.Body).Decode(value) }
