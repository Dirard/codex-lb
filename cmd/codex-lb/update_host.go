package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"codex-lb/internal/adapters/updatecontrol"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

const workerSocketEnv = "CODEX_LB_INTERNAL_WORKER_SOCKET"
const parentSocketEnv = "CODEX_LB_INTERNAL_PARENT_SOCKET"
const controlTokenEnv = "CODEX_LB_INTERNAL_UPDATE_TOKEN"

// A handoff worker inherits the shared listener, the database lock and the
// lifeline as descriptors 3, 4 and 5, and learns its identity from these.
const handoffEnv = "CODEX_LB_INTERNAL_HANDOFF"
const instanceEnv = "CODEX_LB_INTERNAL_INSTANCE"
const peersEnv = "CODEX_LB_INTERNAL_PEERS"
const overlapEnv = "CODEX_LB_INTERNAL_OVERLAP"

const internalEnvPrefix = "CODEX_LB_INTERNAL_"

const (
	listenerDescriptor = 3 + iota
	databaseLockDescriptor
	lifelineDescriptor
)

var errRuntimeNotExecutable = errors.New("update storage does not permit executable files")

type updateChild struct {
	command             *exec.Cmd
	control             *updatecontrol.Client
	done                chan struct{}
	expected            bool
	activationAttempted bool
	err                 error
	instance            string
	handoff             bool
	version             string
	socket              string
}

// updateHost runs the launcher's workers. Outside a switch exactly one worker
// serves (child). A seamless switch adds a candidate, and the previous worker
// keeps draining in retiring after the handover.
type updateHost struct {
	mu                                       sync.Mutex
	child, candidate                         *updateChild
	retiring                                 map[*updateChild]struct{}
	args                                     []string
	dataDir, privateDir, parentSocket, token string
	stdout, stderr                           io.Writer
	fatal                                    chan struct{}
	shutdownTimeout                          time.Duration
	logger                                   *slog.Logger
	// listen is the configured address the host binds for handoff workers.
	listen string
	// listener, lock and lifeline are owned while handoff workers run; the
	// read end of the lifeline reaches EOF in every worker when the host exits.
	listener, lock, lifeline, lifelineRead *os.File
	relaying                               bool
	relayStop                              chan struct{}
	relayDone                              sync.WaitGroup
}

type limitedOutput struct{ bytes.Buffer }

func (b *limitedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 4096 {
		return 0, errors.New("descriptor too large")
	}
	return b.Buffer.Write(p)
}

func (h *updateHost) Inspect(ctx context.Context, path string) (domain.RuntimeDescriptor, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "update-info")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	cmd.Stderr = io.Discard
	var output limitedOutput
	cmd.Stdout = &output
	var descriptor domain.RuntimeDescriptor
	if err := cmd.Run(); err != nil {
		if errors.Is(err, os.ErrPermission) {
			return descriptor, errRuntimeNotExecutable
		}
		return descriptor, errors.New("invalid executable descriptor")
	}
	if json.Unmarshal(output.Bytes(), &descriptor) != nil {
		return descriptor, errors.New("invalid executable descriptor")
	}
	return descriptor, nil
}

func (h *updateHost) log() *slog.Logger {
	if h.logger != nil {
		return h.logger
	}
	return slog.New(slog.DiscardHandler)
}

// own binds the configured address and locks the database inode so both can
// be lent to handoff workers for the host's lifetime.
func (h *updateHost) own() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.listener != nil {
		return nil
	}
	// The lock creates the database file, so the data directory must already
	// be private, exactly as a worker would require it.
	if err := os.MkdirAll(h.dataDir, 0o700); err != nil {
		return errors.New("cannot create data directory")
	}
	if info, err := os.Lstat(h.dataDir); err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("data directory must be a private directory (mode 0700), not a symlink")
	}
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", h.listen)
	if err != nil {
		return listenError("managed listener", h.listen, err)
	}
	tcp, ok := listener.(*net.TCPListener)
	if !ok {
		listener.Close()
		return errors.New("managed listener is not TCP")
	}
	// The duplicate keeps the socket and its accept queue; the original closes.
	shared, err := tcp.File()
	listener.Close()
	if err != nil {
		return errors.New("cannot share the managed listener")
	}
	lock, _, err := lockDatabase(filepath.Join(h.dataDir, "codex-lb.sqlite3"))
	if err != nil {
		shared.Close()
		return err
	}
	read, write, err := os.Pipe()
	if err != nil {
		shared.Close()
		lock.Close()
		return errors.New("cannot create worker lifeline")
	}
	h.listener, h.lock, h.lifelineRead, h.lifeline = shared, lock, read, write
	return nil
}

// release returns the address and the database to a worker that binds and
// locks them itself. Every handoff worker must have exited.
func (h *updateHost) release() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, file := range []*os.File{h.listener, h.lock, h.lifelineRead, h.lifeline} {
		if file != nil {
			file.Close()
		}
	}
	h.listener, h.lock, h.lifelineRead, h.lifeline = nil, nil, nil, nil
}

func randomInstance() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func (h *updateHost) liveInstancesLocked() []string {
	var live []string
	for _, child := range []*updateChild{h.child, h.candidate} {
		if child != nil && child.handoff {
			live = append(live, child.instance)
		}
	}
	for child := range h.retiring {
		if child.handoff {
			live = append(live, child.instance)
		}
	}
	return live
}

// spawn starts a worker for item. A handoff worker receives the shared
// descriptors and, beside another live worker, starts with background deferred.
func (h *updateHost) spawn(item domain.RuntimeInstallation, overlap bool) (*updateChild, error) {
	instance, err := randomInstance()
	if err != nil {
		return nil, errors.New("cannot identify managed worker")
	}
	socket := filepath.Join(h.privateDir, "worker-"+instance[:12]+".sock")
	if info, err := os.Lstat(socket); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("unsafe worker socket")
		}
		if err = os.Remove(socket); err != nil {
			return nil, err
		}
	}
	handoff := item.Descriptor.Handoff >= domain.RuntimeHandoffProtocol
	// The worker outlives request contexts; the host stops it through its own
	// drain protocol instead of a context kill.
	command := exec.Command(item.Path, append([]string{"serve"}, h.args...)...) //nolint:noctx
	command.Env = workerEnvironment(socket, h.parentSocket, h.token)
	command.Stdout, command.Stderr = h.stdout, h.stderr
	child := &updateChild{command: command, control: updatecontrol.NewClient(socket, h.token), done: make(chan struct{}), expected: true,
		instance: instance, handoff: handoff, version: item.Descriptor.Version, socket: socket}
	h.mu.Lock()
	if handoff {
		if h.listener == nil {
			h.mu.Unlock()
			child.control.Close()
			return nil, errors.New("managed listener is not owned")
		}
		command.Env = append(command.Env, handoffEnv+"=1", instanceEnv+"="+instance, peersEnv+"="+strings.Join(h.liveInstancesLocked(), ","))
		if overlap {
			command.Env = append(command.Env, overlapEnv+"=1")
		}
		command.ExtraFiles = []*os.File{h.listener, h.lock, h.lifelineRead}
	} else {
		command.SysProcAttr = updateProcessAttributes()
	}
	h.mu.Unlock()
	started := make(chan error, 1)
	go func() {
		if !handoff {
			// Linux Pdeathsig follows the creating OS thread, not just the PID.
			// Keep that thread alive for the worker's entire lifetime. Handoff
			// workers follow the lifeline instead, independent of any thread.
			goruntime.LockOSThread()
			defer goruntime.UnlockOSThread()
		}
		if err := command.Start(); err != nil {
			started <- err
			return
		}
		started <- nil
		err := command.Wait()
		h.childExited(child, err)
	}()
	if err := <-started; err != nil {
		child.control.Close()
		return nil, errors.New("cannot execute managed worker")
	}
	return child, nil
}

// childExited records a worker's end. An unexpected exit of the serving worker
// is fatal; the end of any other handoff worker lets the serving one mark that
// worker's unsettled reservations as interrupted.
func (h *updateHost) childExited(child *updateChild, err error) {
	h.mu.Lock()
	child.err = err
	unexpected := !child.expected
	close(child.done)
	delete(h.retiring, child)
	active := h.child
	live := h.liveInstancesLocked()
	h.mu.Unlock()
	if unexpected {
		select {
		case h.fatal <- struct{}{}:
		default:
		}
		return
	}
	if child != active && child.handoff && active != nil && active.handoff {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := active.control.Call(ctx, "POST", "/peer-exited", peerExit{Live: live}, nil); err != nil {
				h.log().Warn("could not report the retired worker's exit", "version", child.version)
			}
		}()
	}
}

// workerReadyTimeout bounds a worker's start, which includes its migrations:
// 33 → 41 on a 1.3 GB production database took 8 s, so a large or slow disk
// keeps a wide margin before a migrating worker would be given up.
const workerReadyTimeout = 10 * time.Minute

func (h *updateHost) awaitReady(ctx context.Context, child *updateChild, version string) error {
	ready, cancel := context.WithTimeout(ctx, workerReadyTimeout)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		var status workerUpdateStatus
		if err := child.control.Call(ready, "GET", "/ready", nil, &status); err == nil && status.Prepared && status.Version == version {
			return nil
		}
		select {
		case <-child.done:
			return errors.New("managed worker exited before readiness")
		case <-ready.Done():
			return errors.New("managed worker readiness timeout")
		case <-ticker.C:
		}
	}
}

// PrepareStart starts the only worker of a classic switch or of the boot. A
// worker that cannot inherit descriptors gets the address and database lock
// released to it, after every handoff worker has exited.
func (h *updateHost) PrepareStart(ctx context.Context, item domain.RuntimeInstallation) error {
	h.mu.Lock()
	if h.child != nil || h.candidate != nil {
		h.mu.Unlock()
		return errors.New("worker is already running")
	}
	h.mu.Unlock()
	if item.Descriptor.Handoff >= domain.RuntimeHandoffProtocol {
		if err := h.own(); err != nil {
			return err
		}
	} else {
		if err := h.stopRetiring(ctx); err != nil {
			return err
		}
		h.release()
	}
	child, err := h.spawn(item, false)
	if err != nil {
		return err
	}
	h.mu.Lock()
	h.child = child
	h.mu.Unlock()
	return h.awaitReady(ctx, child, item.Descriptor.Version)
}

func workerEnvironment(socket, parent, token string) []string {
	values := make([]string, 0, len(os.Environ())+3)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, internalEnvPrefix) {
			continue
		}
		values = append(values, entry)
	}
	return append(values, workerSocketEnv+"="+socket, parentSocketEnv+"="+parent, controlTokenEnv+"="+token)
}

func (h *updateHost) current() *updateChild { h.mu.Lock(); defer h.mu.Unlock(); return h.child }

func (h *updateHost) Activate(ctx context.Context) error {
	child := h.current()
	if child == nil {
		return errors.New("no managed worker")
	}
	h.mu.Lock()
	child.activationAttempted = true
	h.mu.Unlock()
	if err := activateWorker(ctx, child); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	select {
	case <-child.done:
		return errors.New("managed worker exited during activation")
	default:
		child.expected = false
		return nil
	}
}

func activateWorker(ctx context.Context, child *updateChild) error {
	err := child.control.Call(ctx, "POST", "/activate", nil, nil)
	var status workerUpdateStatus
	if verify := child.control.Call(ctx, "GET", "/ready", nil, &status); verify != nil || !status.Serving {
		if err != nil {
			return err
		}
		return errors.New("worker activation was not verified")
	}
	return nil
}

// Overlap reports whether a handoff candidate can start beside the serving worker.
func (h *updateHost) Overlap() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.child == nil || !h.child.handoff || h.listener == nil || h.candidate != nil {
		return false
	}
	select {
	case <-h.child.done:
		return false
	default:
		return true
	}
}

func (h *updateHost) StartBeside(ctx context.Context, item domain.RuntimeInstallation) error {
	if !h.Overlap() || item.Descriptor.Handoff < domain.RuntimeHandoffProtocol {
		return errors.New("seamless switching is unavailable")
	}
	child, err := h.spawn(item, true)
	if err != nil {
		return err
	}
	h.mu.Lock()
	h.candidate = child
	h.mu.Unlock()
	h.startRelay()
	if err := h.awaitReady(ctx, child, item.Descriptor.Version); err != nil {
		_ = h.DiscardCandidate(context.WithoutCancel(ctx))
		return err
	}
	return nil
}

// Handover makes the candidate accept on the shared socket before the serving
// worker stops accepting, so the kernel queue never lacks an acceptor. Background
// work moves only after the previous worker joined its own loops.
func (h *updateHost) Handover(ctx context.Context) error {
	h.mu.Lock()
	candidate, previous := h.candidate, h.child
	if candidate != nil {
		candidate.activationAttempted = true
	}
	h.mu.Unlock()
	if candidate == nil || previous == nil {
		return errors.New("no seamless candidate")
	}
	if err := activateWorker(ctx, candidate); err != nil {
		_ = h.DiscardCandidate(context.WithoutCancel(ctx))
		return err
	}
	h.mu.Lock()
	select {
	case <-candidate.done:
		h.candidate = nil
		h.mu.Unlock()
		return errors.New("managed worker exited during activation")
	default:
	}
	h.child, h.candidate = candidate, nil
	candidate.expected = false
	previous.expected = true
	if h.retiring == nil {
		h.retiring = make(map[*updateChild]struct{})
	}
	h.retiring[previous] = struct{}{}
	h.mu.Unlock()
	retire, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	err := previous.control.Call(retire, "POST", "/retire", nil, nil)
	cancel()
	if err != nil {
		// Without the acknowledgement its loops may still run: end it through
		// the ordinary drain and wait before starting the candidate's loops.
		h.log().Warn("previous worker did not acknowledge retirement; terminating it", "version", previous.version)
		h.terminate(previous)
	}
	for attempt := 0; attempt < 3; attempt++ {
		call, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		err = candidate.control.Call(call, "POST", "/background", nil, nil)
		cancel()
		if err == nil {
			break
		}
	}
	if err != nil {
		h.log().Error("new worker did not start background work", "version", candidate.version)
	}
	h.log().Info("seamless switch complete", "version", candidate.version, "retiring_version", previous.version)
	return nil
}

// terminate stops a worker through SIGTERM (its ordinary bounded drain) and
// joins it, killing it only after the shutdown deadline.
func (h *updateHost) terminate(child *updateChild) {
	h.mu.Lock()
	child.expected = true
	h.mu.Unlock()
	_ = child.command.Process.Signal(syscall.SIGTERM)
	timeout := h.shutdownTimeout
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	select {
	case <-child.done:
	case <-time.After(timeout):
		_ = child.command.Process.Kill()
		<-child.done
	}
	child.control.Close()
}

func (h *updateHost) DiscardCandidate(context.Context) error {
	h.mu.Lock()
	candidate := h.candidate
	h.candidate = nil
	h.mu.Unlock()
	if candidate != nil {
		h.terminate(candidate)
	}
	return nil
}

// stopRetiring ends workers still finishing earlier requests; a worker that
// binds and locks by itself cannot start while they hold the descriptors.
func (h *updateHost) stopRetiring(ctx context.Context) error {
	h.mu.Lock()
	retiring := make([]*updateChild, 0, len(h.retiring))
	for child := range h.retiring {
		retiring = append(retiring, child)
	}
	h.mu.Unlock()
	for _, child := range retiring {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		h.terminate(child)
	}
	return nil
}

func (h *updateHost) WaitForIdle(ctx context.Context) (result error) {
	child := h.current()
	if child == nil {
		return errors.New("no managed worker")
	}
	// A lost acknowledgement can follow a successful gate. Explicitly cancel
	// that gate on failure, including the deadline, so the server stays usable.
	defer func() {
		if result != nil {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			_ = child.control.Call(cleanup, "POST", "/cancel-stop", nil, nil)
		}
	}()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		err := child.control.Call(ctx, "POST", "/prepare-stop", nil, nil)
		if err == nil {
			return nil
		}
		if !errors.Is(err, domain.ErrUpdateBusy) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-child.done:
			return errors.New("managed worker stopped")
		case <-ticker.C:
		}
	}
}

func (h *updateHost) Stop(ctx context.Context) (result error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	child := h.current()
	if child == nil {
		return nil
	}
	h.mu.Lock()
	wasServing := child.activationAttempted
	wasExpected := child.expected
	child.expected = true
	h.mu.Unlock()
	defer func() {
		if result != nil {
			h.mu.Lock()
			child.expected = wasExpected
			h.mu.Unlock()
		}
	}()
	select {
	case <-child.done:
	default:
		if !wasServing {
			_ = child.command.Process.Signal(syscall.SIGTERM)
		} else {
			// Commit shutdown inside the worker's idle lease. If it expired,
			// obtain another gate rather than signal a newly active request.
			for {
				if err := h.WaitForIdle(ctx); err != nil {
					return err
				}
				err := child.control.Call(ctx, "POST", "/stop", nil, nil)
				if errors.Is(err, domain.ErrUpdateBusy) {
					continue
				}
				if err != nil {
					select {
					case <-child.done:
					default:
						return err
					}
				}
				break
			}
		}
	}
	select {
	case <-child.done:
	case <-ctx.Done():
		if wasServing {
			return ctx.Err()
		}
		// A fenced candidate has never admitted work. Kill and join it after
		// the graceful deadline before restoring the previous database owner.
		_ = child.command.Process.Kill()
		<-child.done
	}
	child.control.Close()
	h.mu.Lock()
	h.child = nil
	err := child.err
	h.mu.Unlock()
	if err != nil && wasServing {
		return errors.New("managed worker exited uncleanly")
	}
	return nil
}

func (h *updateHost) exited() bool {
	child := h.current()
	if child == nil {
		return true
	}
	select {
	case <-child.done:
		return true
	default:
		return false
	}
}

// Shutdown is an operator/process shutdown, not a planned update. Preserve the
// runtime's existing bounded graceful SIGTERM behavior rather than waiting for idle.
func (h *updateHost) Shutdown() error {
	h.stopRelay()
	h.mu.Lock()
	children := make([]*updateChild, 0, 2+len(h.retiring))
	for _, child := range []*updateChild{h.child, h.candidate} {
		if child != nil {
			children = append(children, child)
		}
	}
	for child := range h.retiring {
		children = append(children, child)
	}
	h.mu.Unlock()
	var wait sync.WaitGroup
	for _, child := range children {
		wait.Add(1)
		go func() { defer wait.Done(); h.terminate(child) }()
	}
	wait.Wait()
	h.mu.Lock()
	h.child, h.candidate = nil, nil
	h.mu.Unlock()
	h.release()
	return nil
}

type peerExit struct {
	Live []string `json:"live"`
}

// workerLoadReport is one worker's admission pressure and resident memory.
type workerLoadReport struct {
	Load          application.WorkerLoad `json:"load"`
	ResidentBytes int64                  `json:"residentBytes"`
}

func (r workerLoadReport) add(other workerLoadReport) workerLoadReport {
	return workerLoadReport{Load: r.Load.Add(other.Load), ResidentBytes: r.ResidentBytes + other.ResidentBytes}
}

func (h *updateHost) liveWorkers() []*updateChild {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.liveWorkersLocked()
}

func (h *updateHost) liveWorkersLocked() []*updateChild {
	var workers []*updateChild
	for _, child := range []*updateChild{h.child, h.candidate} {
		if child != nil && child.handoff {
			workers = append(workers, child)
		}
	}
	for child := range h.retiring {
		if child.handoff {
			workers = append(workers, child)
		}
	}
	live := workers[:0]
	for _, child := range workers {
		select {
		case <-child.done:
		default:
			live = append(live, child)
		}
	}
	return live
}

// startRelay keeps every live worker informed of the others' load until one
// worker remains, which then receives an empty peer load.
func (h *updateHost) startRelay() {
	h.mu.Lock()
	if h.relaying {
		h.mu.Unlock()
		return
	}
	h.relaying = true
	if h.relayStop == nil {
		h.relayStop = make(chan struct{})
	}
	stop := h.relayStop
	h.relayDone.Add(1)
	h.mu.Unlock()
	go func() {
		defer h.relayDone.Done()
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		last := map[*updateChild]workerLoadReport{}
		for {
			workers := h.liveWorkers()
			if len(workers) < 2 {
				for _, child := range workers {
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					_ = child.control.Call(ctx, "PUT", "/peer-load", workerLoadReport{}, nil)
					cancel()
				}
				// A worker started meanwhile relied on this loop; keep going.
				h.mu.Lock()
				if len(h.liveWorkersLocked()) >= 2 {
					h.mu.Unlock()
					continue
				}
				h.relaying = false
				h.mu.Unlock()
				return
			}
			reports := make(map[*updateChild]workerLoadReport, len(workers))
			for _, child := range workers {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				var report workerLoadReport
				if err := child.control.Call(ctx, "GET", "/load", nil, &report); err == nil {
					last[child] = report
				}
				cancel()
				reports[child] = last[child]
			}
			for _, child := range workers {
				var peers workerLoadReport
				for other, report := range reports {
					if other != child {
						peers = peers.add(report)
					}
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				_ = child.control.Call(ctx, "PUT", "/peer-load", peers, nil)
				cancel()
			}
			select {
			case <-stop:
				return
			case <-ticker.C:
			}
		}
	}()
}

func (h *updateHost) stopRelay() {
	h.mu.Lock()
	if h.relayStop == nil {
		h.relayStop = make(chan struct{})
	}
	select {
	case <-h.relayStop:
	default:
		close(h.relayStop)
	}
	h.mu.Unlock()
	h.relayDone.Wait()
}
