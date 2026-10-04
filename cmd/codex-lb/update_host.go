package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"codex-lb/internal/adapters/updatecontrol"
	"codex-lb/internal/domain"
)

const workerSocketEnv = "CODEX_LB_INTERNAL_WORKER_SOCKET"
const parentSocketEnv = "CODEX_LB_INTERNAL_PARENT_SOCKET"
const controlTokenEnv = "CODEX_LB_INTERNAL_UPDATE_TOKEN"

var errRuntimeNotExecutable = errors.New("update storage does not permit executable files")

type updateChild struct {
	command             *exec.Cmd
	control             *updatecontrol.Client
	done                chan struct{}
	expected            bool
	activationAttempted bool
	err                 error
}

type updateHost struct {
	mu                                       sync.Mutex
	child                                    *updateChild
	args                                     []string
	dataDir, privateDir, parentSocket, token string
	stdout, stderr                           io.Writer
	fatal                                    chan struct{}
	shutdownTimeout                          time.Duration
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

func (h *updateHost) PrepareStart(ctx context.Context, item domain.RuntimeInstallation) error {
	h.mu.Lock()
	if h.child != nil {
		h.mu.Unlock()
		return errors.New("worker is already running")
	}
	h.mu.Unlock()
	socket := filepath.Join(h.privateDir, "worker.sock")
	if info, err := os.Lstat(socket); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("unsafe worker socket")
		}
		if err = os.Remove(socket); err != nil {
			return err
		}
	}
	command := exec.Command(item.Path, append([]string{"serve"}, h.args...)...)
	command.Env = workerEnvironment(socket, h.parentSocket, h.token)
	command.Stdout, command.Stderr = h.stdout, h.stderr
	command.SysProcAttr = updateProcessAttributes()
	child := &updateChild{command: command, control: updatecontrol.NewClient(socket, h.token), done: make(chan struct{}), expected: true}
	started := make(chan error, 1)
	go func() {
		// Linux Pdeathsig follows the creating OS thread, not just the PID.
		// Keep that thread alive for the worker's entire lifetime.
		goruntime.LockOSThread()
		defer goruntime.UnlockOSThread()
		if err := command.Start(); err != nil {
			started <- err
			return
		}
		h.mu.Lock()
		h.child = child
		h.mu.Unlock()
		started <- nil
		err := command.Wait()
		h.mu.Lock()
		child.err = err
		unexpected := !child.expected
		close(child.done)
		h.mu.Unlock()
		if unexpected {
			select {
			case h.fatal <- struct{}{}:
			default:
			}
		}
	}()
	if err := <-started; err != nil {
		child.control.Close()
		return errors.New("cannot execute managed worker")
	}
	ready, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		var status workerUpdateStatus
		if err := child.control.Call(ready, "GET", "/ready", nil, &status); err == nil && status.Prepared && status.Version == item.Descriptor.Version {
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

func workerEnvironment(socket, parent, token string) []string {
	values := make([]string, 0, len(os.Environ())+3)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, workerSocketEnv+"=") || strings.HasPrefix(entry, parentSocketEnv+"=") || strings.HasPrefix(entry, controlTokenEnv+"=") {
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
	err := child.control.Call(ctx, "POST", "/activate", nil, nil)
	var status workerUpdateStatus
	if verify := child.control.Call(ctx, "GET", "/ready", nil, &status); verify != nil || !status.Serving {
		if err != nil {
			return err
		}
		return errors.New("worker activation was not verified")
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
	child := h.current()
	if child == nil {
		return nil
	}
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
	h.mu.Lock()
	h.child = nil
	h.mu.Unlock()
	return nil
}
