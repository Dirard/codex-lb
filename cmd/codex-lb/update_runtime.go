package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"syscall"
	"time"

	"codex-lb/internal/adapters/releases"
	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/adapters/updatecontrol"
	"codex-lb/internal/adapters/updatefiles"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

func runtimeDescriptor() domain.RuntimeDescriptor {
	return domain.RuntimeDescriptor{Version: version, GOOS: goruntime.GOOS, GOARCH: goruntime.GOARCH, SchemaVersion: sqlite.SchemaVersion(), UpdateProtocol: domain.RuntimeUpdateProtocol}
}

// serveManagedRuntime owns one worker and its private updater, independent of
// whichever external process manager (if any) launched the binary.
func serveManagedRuntime(ctx context.Context, cfg config, args []string, stdout, stderr io.Writer, logger *slog.Logger) error {
	if goruntime.GOOS != "linux" || (goruntime.GOARCH != "amd64" && goruntime.GOARCH != "arm64") {
		return serveWithoutUpdates(ctx, cfg, logger, "Self-update releases are available for Linux amd64 and arm64")
	}
	if _, stable := domain.ParseRuntimeVersion(version); !stable {
		return serveWithoutUpdates(ctx, cfg, logger, "Self-update requires a versioned release build")
	}
	if _, port, _ := net.SplitHostPort(cfg.listen); port == "0" {
		return serveWithoutUpdates(ctx, cfg, logger, "Self-update requires a fixed listening port")
	}
	store, err := updatefiles.New(cfg.dataDir)
	if err != nil {
		_, stateErr := os.Lstat(filepath.Join(cfg.dataDir, "updates", "state.json"))
		if (errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EROFS)) && errors.Is(stateErr, os.ErrNotExist) {
			return serveWithoutUpdates(ctx, cfg, logger, "Private update storage is not writable")
		}
		return errors.New("cannot open private runtime update storage")
	}
	defer store.Close()
	executable, err := os.Executable()
	if err != nil {
		return errors.New("cannot locate runtime executable")
	}
	state, err := store.Initialize(ctx, executable, runtimeDescriptor())
	if err != nil {
		return errors.New("cannot initialize managed runtime version")
	}
	privateDir, err := os.MkdirTemp("", "codex-lb-control-")
	if err != nil {
		return errors.New("cannot create private runtime control")
	}
	defer os.RemoveAll(privateDir)
	secret := make([]byte, 32)
	if _, err = rand.Read(secret); err != nil {
		return errors.New("cannot initialize private runtime control")
	}
	token := hex.EncodeToString(secret)
	parentSocket := filepath.Join(privateDir, "parent.sock")
	listener, err := net.Listen("unix", parentSocket)
	if err != nil {
		return errors.New("cannot open private runtime control")
	}
	defer listener.Close()
	_ = os.Chmod(parentSocket, 0600)
	host := &updateHost{args: args, dataDir: cfg.dataDir, privateDir: privateDir, parentSocket: parentSocket, token: token, stdout: stdout, stderr: stderr, fatal: make(chan struct{}, 1)}
	host.shutdownTimeout = cfg.shutdownGrace + time.Minute
	defer host.Shutdown()
	if err = prepareManagedBoot(ctx, store, host, &state); err != nil {
		if errors.Is(err, errRuntimeNotExecutable) && state.Current.Descriptor == runtimeDescriptor() && updatefiles.MatchesExecutable(ctx, executable, state.Current.SHA256) {
			return serveWithoutUpdates(ctx, cfg, logger, "Update storage does not permit executable files")
		}
		return err
	}
	service := application.NewRuntimeUpdates(ctx, releases.New(nil, goruntime.GOOS, goruntime.GOARCH), store, host, state, application.RuntimeUpdateConfig{})
	control := &http.Server{Handler: updatecontrol.Protect(token, updatecontrol.Handler(service)), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, MaxHeaderBytes: 4096}
	controlDone := make(chan struct{})
	go func() { defer close(controlDone); _ = control.Serve(listener) }()
	defer func() { _ = control.Close(); <-controlDone }()
	if err = host.Activate(ctx); err != nil {
		_ = service.Close()
		return errors.New("managed runtime activation failed")
	}
	background, cancel := context.WithCancel(ctx)
	defer cancel()
	checksDone := make(chan struct{})
	go func() {
		defer close(checksDone)
		if _, stable := domain.ParseRuntimeVersion(state.Current.Descriptor.Version); stable {
			service.RunChecks(background)
		}
	}()
	defer func() { cancel(); _ = service.Close(); <-checksDone }()
	monitor := time.NewTicker(time.Second)
	defer monitor.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-host.fatal:
			return errors.New("managed runtime stopped unexpectedly")
		case <-monitor.C:
			status, _ := service.Status(ctx)
			if status.Phase == "failed" && host.exited() {
				return errors.New("runtime recovery failed; manual recovery is required")
			}
		}
	}
}

func prepareManagedBoot(ctx context.Context, store *updatefiles.Store, host *updateHost, state *domain.RuntimeInstallState) error {
	start := func(item domain.RuntimeInstallation) error {
		if err := store.Verify(ctx, item); err != nil {
			return err
		}
		descriptor, err := host.Inspect(ctx, item.Path)
		if err != nil {
			return err
		}
		if descriptor != item.Descriptor {
			return errors.New("managed executable descriptor mismatch")
		}
		return host.PrepareStart(ctx, item)
	}
	if err := start(state.Current); err == nil {
		if state.Pending != nil || (state.Phase != "idle" && state.Phase != "succeeded" && state.Phase != "failed") {
			state.Pending = nil
			state.Phase = "failed"
			state.LastError = "Interrupted update; the committed version was verified on restart"
			return store.Save(ctx, *state)
		}
		return nil
	} else if errors.Is(err, errRuntimeNotExecutable) {
		return err
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 90*time.Second)
	defer cancel()
	if host.Stop(cleanup) != nil {
		return errors.New("failed startup could not be stopped safely")
	}
	if state.Previous == nil || state.Previous.Descriptor.SchemaVersion != state.Current.Descriptor.SchemaVersion {
		return errors.New("managed runtime startup failed; no compatible previous version")
	}
	previous := *state.Previous
	if err := start(previous); err != nil {
		_ = host.Stop(cleanup)
		return errors.New("previous runtime startup also failed")
	}
	state.Current, state.Previous, state.Pending = previous, nil, nil
	state.Phase, state.LastError = "failed", "Failed runtime startup; previous version restored"
	if err := store.Save(ctx, *state); err != nil {
		_ = host.Stop(cleanup)
		return errors.New("cannot persist runtime recovery")
	}
	return nil
}
