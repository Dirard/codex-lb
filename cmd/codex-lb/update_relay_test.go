package main

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"codex-lb/internal/adapters/updatecontrol"
	"codex-lb/internal/application"
)

// fakeLoadWorker serves only the load exchange of a handoff worker.
type fakeLoadWorker struct {
	mu       sync.Mutex
	own      workerLoadReport
	received []workerLoadReport
}

func (w *fakeLoadWorker) serve(t *testing.T, socket, token string) {
	t.Helper()
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /load", func(rw http.ResponseWriter, _ *http.Request) {
		w.mu.Lock()
		defer w.mu.Unlock()
		_ = json.NewEncoder(rw).Encode(w.own)
	})
	mux.HandleFunc("PUT /peer-load", func(rw http.ResponseWriter, request *http.Request) {
		var report workerLoadReport
		if json.NewDecoder(request.Body).Decode(&report) != nil {
			http.Error(rw, "invalid", http.StatusBadRequest)
			return
		}
		w.mu.Lock()
		w.received = append(w.received, report)
		w.mu.Unlock()
		rw.WriteHeader(http.StatusNoContent)
	})
	server := &http.Server{Handler: updatecontrol.Protect(token, mux)}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
}

func (w *fakeLoadWorker) last() (workerLoadReport, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.received) == 0 {
		return workerLoadReport{}, 0
	}
	return w.received[len(w.received)-1], len(w.received)
}

func TestRelaySendsEachWorkerItsPeersLoad(t *testing.T) {
	t.Parallel()
	dir, err := os.MkdirTemp("", "lb-relay-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	token := strings.Repeat("b", 64)
	active := &fakeLoadWorker{own: workerLoadReport{Load: application.WorkerLoad{Active: 1}, ResidentBytes: 100}}
	retiring := &fakeLoadWorker{own: workerLoadReport{Load: application.WorkerLoad{Active: 3, Queued: 2,
		Accounts: map[string]application.AccountLoad{"acct": {Creates: 1, Streams: 3, Keys: map[string]int{"key": 3}}}}, ResidentBytes: 700}}
	child := func(name string, worker *fakeLoadWorker) *updateChild {
		socket := filepath.Join(dir, name+".sock")
		worker.serve(t, socket, token)
		return &updateChild{control: updatecontrol.NewClient(socket, token), done: make(chan struct{}), handoff: true, instance: strings.Repeat(name[:1], 32)}
	}
	activeChild, retiringChild := child("a", active), child("r", retiring)
	host := &updateHost{child: activeChild, retiring: map[*updateChild]struct{}{retiringChild: {}}}
	t.Cleanup(host.stopRelay)
	host.startRelay()

	deadline := time.Now().Add(5 * time.Second)
	for {
		toActive, _ := active.last()
		toRetiring, _ := retiring.last()
		if toActive.Load.Active == 3 && toRetiring.Load.Active == 1 {
			if toActive.Load.Queued != 2 || toActive.ResidentBytes != 700 || toActive.Load.Accounts["acct"].Keys["key"] != 3 || toRetiring.ResidentBytes != 100 {
				t.Fatalf("peer loads were not relayed intact: active got %+v, retiring got %+v", toActive, toRetiring)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("relay did not deliver peer loads: active got %+v, retiring got %+v", toActive, toRetiring)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Once the retiring worker is gone the survivor's peer load is cleared.
	close(retiringChild.done)
	host.mu.Lock()
	delete(host.retiring, retiringChild)
	host.mu.Unlock()
	for {
		report, _ := active.last()
		if report.Load.Active == 0 && report.ResidentBytes == 0 && len(report.Load.Accounts) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("relay did not clear the survivor's peer load: %+v", report)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
