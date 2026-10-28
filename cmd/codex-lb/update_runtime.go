package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"syscall"
	"time"

	"codex-lb/internal/adapters/releases"
	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/adapters/updatecontrol"
	"codex-lb/internal/adapters/updatefiles"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

// handoffSupport is a link-time switch: -X main.handoffSupport=off builds a
// worker that behaves like releases before the handoff protocol, for
// compatibility tests. Operators cannot change it.
var handoffSupport = "on"

func runtimeDescriptor() domain.RuntimeDescriptor {
	descriptor := domain.RuntimeDescriptor{Version: version, GOOS: goruntime.GOOS, GOARCH: goruntime.GOARCH, SchemaVersion: sqlite.SchemaVersion(), UpdateProtocol: domain.RuntimeUpdateProtocol}
	if handoffSupport == "on" {
		descriptor.Handoff, descriptor.SchemaFloor = domain.RuntimeHandoffProtocol, sqlite.SchemaFloor()
	}
	return descriptor
}

// delegatedEnv carries the digest of the selected binary a bootstrap re-executed
// as the launcher, so it loads the state instead of adopting itself.
const delegatedEnv = "CODEX_LB_INTERNAL_DELEGATED"

// delegationTarget selects the installed version to run as the launcher: newer
// than this binary, handoff-capable, verified, and not a delegation that
// previously failed to confirm.
func delegationTarget(ctx context.Context, store *updatefiles.Store, state *domain.RuntimeInstallState, logger *slog.Logger) (domain.RuntimeInstallation, bool) {
	current := state.Current
	if !domain.NewerRuntimeVersion(current.Descriptor.Version, version) {
		return current, false
	}
	inspected, err := inspectRetained(ctx, store, &updateHost{}, current)
	if err != nil || inspected.Descriptor.Handoff < domain.RuntimeHandoffProtocol {
		return current, false
	}
	previous, err := store.LoadDelegation()
	if err != nil {
		return current, false
	}
	if previous.SHA256 == current.SHA256 && !previous.Confirmed {
		if !previous.Failed {
			logger.Warn("the launcher of the selected version did not start; using the bootstrap launcher", "selected_version", current.Descriptor.Version)
			_ = store.SaveDelegation(updatefiles.LauncherDelegation{SHA256: current.SHA256, Failed: true})
			state.Phase, state.LastError = "failed", "The launcher of "+current.Descriptor.Version+" did not start; the installed bootstrap launcher is used"
			_ = store.Save(ctx, *state)
		}
		return current, false
	}
	if err := store.SaveDelegation(updatefiles.LauncherDelegation{SHA256: current.SHA256}); err != nil {
		return current, false
	}
	return inspected, true
}

// delegateLauncher replaces this process image with the selected version,
// keeping the process id a service manager tracks. It returns only on failure.
func delegateLauncher(target domain.RuntimeInstallation, logger *slog.Logger) {
	logger.Info("delegating the launcher to the selected version", "bootstrap_version", version, "selected_version", target.Descriptor.Version)
	environment := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, internalEnvPrefix) {
			environment = append(environment, entry)
		}
	}
	environment = append(environment, delegatedEnv+"="+target.SHA256)
	err := syscall.Exec(target.Path, append([]string{target.Path}, os.Args[1:]...), environment)
	logger.Warn("launcher delegation failed; using the bootstrap launcher", "error_type", fmt.Sprintf("%T", err))
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
	delegated := os.Getenv(delegatedEnv)
	var state domain.RuntimeInstallState
	adopted := false // an operator replaced the bootstrap executable
	if delegated != "" && updatefiles.MatchesExecutable(ctx, executable, delegated) {
		// A bootstrap already initialized the state and selected this binary.
		state, err = store.Load(ctx)
	} else {
		delegated = ""
		before, loadErr := store.Load(ctx)
		state, err = store.Initialize(ctx, executable, runtimeDescriptor())
		adopted = err == nil && loadErr == nil && before.Current.SHA256 != state.Current.SHA256
	}
	if err != nil {
		return errors.New("cannot initialize managed runtime version")
	}
	if delegated == "" {
		if target, ok := delegationTarget(ctx, store, &state, logger); ok {
			_ = store.Close()
			delegateLauncher(target, logger)
			// Exec failed: keep serving with this launcher.
			if store, err = updatefiles.New(cfg.dataDir); err != nil {
				return errors.New("cannot open private runtime update storage")
			}
			defer store.Close()
			_ = store.SaveDelegation(updatefiles.LauncherDelegation{SHA256: target.SHA256, Failed: true})
		}
	}
	selected := []any{"bootstrap_version", version, "selected_version", state.Current.Descriptor.Version,
		"selected_sha256", state.Current.SHA256, "bootstrap_sha256", state.BootstrapSHA256, "delegated", delegated != ""}
	if state.Previous != nil {
		selected = append(selected, "previous_version", state.Previous.Descriptor.Version)
	}
	logger.Info("managed runtime selected", selected...)
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
	listener, err := (&net.ListenConfig{}).Listen(ctx, "unix", parentSocket)
	if err != nil {
		return errors.New("cannot open private runtime control")
	}
	defer listener.Close()
	_ = os.Chmod(parentSocket, 0600)
	host := &updateHost{args: args, dataDir: cfg.dataDir, privateDir: privateDir, parentSocket: parentSocket, token: token, stdout: stdout, stderr: stderr, fatal: make(chan struct{}, 1),
		listen: cfg.listen, logger: logger}
	host.shutdownTimeout = cfg.shutdownGrace + time.Minute
	defer host.Shutdown()
	if adoptionMigrates(adopted, state) {
		// A manually installed newer bootstrap migrates the database when its
		// worker opens it; keep the pre-migration state like an update does.
		if err = backupExistingDatabase(ctx, host, cfg.dataDir); err != nil {
			return errors.New("cannot back up the database before the replaced bootstrap migrates it")
		}
		logger.Info("pre-migration backup taken for the replaced bootstrap", "previous_version", state.Previous.Descriptor.Version, "selected_version", state.Current.Descriptor.Version)
	}
	if err = prepareManagedBoot(ctx, store, host, &state); err != nil {
		if errors.Is(err, errRuntimeNotExecutable) && state.Current.Descriptor.Identity() == runtimeDescriptor().Identity() && updatefiles.MatchesExecutable(ctx, executable, state.Current.SHA256) {
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
	if delegated != "" {
		if err := store.SaveDelegation(updatefiles.LauncherDelegation{SHA256: delegated, Confirmed: true}); err != nil {
			logger.Warn("could not confirm the launcher delegation")
		}
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

// adoptionMigrates reports whether an adopted bootstrap will migrate the
// database beyond the schema of the version it replaced.
func adoptionMigrates(adopted bool, state domain.RuntimeInstallState) bool {
	return adopted && state.Previous != nil && state.Current.Descriptor.SchemaVersion > state.Previous.Descriptor.SchemaVersion
}

// backupExistingDatabase takes the pre-update backup unless there is no data yet.
func backupExistingDatabase(ctx context.Context, host *updateHost, dataDir string) error {
	info, err := os.Lstat(filepath.Join(dataDir, "codex-lb.sqlite3"))
	if errors.Is(err, os.ErrNotExist) || (err == nil && info.Size() == 0) {
		return nil
	}
	return host.Backup(ctx)
}

// inspectRetained verifies a retained executable and returns it with the
// capabilities it reports; an older launcher may have stored it without them.
func inspectRetained(ctx context.Context, store *updatefiles.Store, host *updateHost, item domain.RuntimeInstallation) (domain.RuntimeInstallation, error) {
	if err := store.Verify(ctx, item); err != nil {
		return item, err
	}
	descriptor, err := host.Inspect(ctx, item.Path)
	if err != nil {
		return item, err
	}
	if descriptor.Identity() != item.Descriptor.Identity() {
		return item, errors.New("managed executable descriptor mismatch")
	}
	item.Descriptor = descriptor
	return item, nil
}

func prepareManagedBoot(ctx context.Context, store *updatefiles.Store, host *updateHost, state *domain.RuntimeInstallState) error {
	start := func(item *domain.RuntimeInstallation) error {
		inspected, err := inspectRetained(ctx, store, host, *item)
		if err != nil {
			return err
		}
		*item = inspected
		return host.PrepareStart(ctx, inspected)
	}
	if state.Previous != nil {
		if previous, err := inspectRetained(ctx, store, host, *state.Previous); err == nil {
			state.Previous = &previous
		}
	}
	if err := start(&state.Current); err == nil {
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
	if state.Previous == nil {
		return errors.New("managed runtime startup failed; no compatible previous version")
	}
	if !domain.SharedSchema(state.Current.Descriptor, state.Previous.Descriptor) {
		// Traffic may have reached the newer schema, so the pre-update backup is
		// never restored automatically here.
		return errors.New("managed runtime startup failed; no compatible previous version (it needs the pre-update database backup in updates/)")
	}
	previous := *state.Previous
	if err := start(&previous); err != nil {
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
