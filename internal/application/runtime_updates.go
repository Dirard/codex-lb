package application

import (
	"context"
	"sync"
	"time"

	"codex-lb/internal/domain"
)

type RuntimeUpdateConfig struct {
	WaitTimeout   time.Duration
	CheckInterval time.Duration
}

// RuntimeUpdates realizes the update/rollback workflows without depending on a
// service manager. Every job is owned and joined by the launcher context.
type RuntimeUpdates struct {
	mu                       sync.Mutex
	source                   RuntimeReleaseSource
	store                    RuntimeInstallationStore
	host                     RuntimeUpdateHost
	config                   RuntimeUpdateConfig
	state                    domain.RuntimeInstallState
	latest                   domain.RuntimeRelease
	checkedAt                *time.Time
	nextCheck                time.Time
	phase, target, lastError string
	busy                     bool
	ctx                      context.Context
	cancel                   context.CancelFunc
	jobs                     sync.WaitGroup
	closed                   bool
}

func NewRuntimeUpdates(ctx context.Context, source RuntimeReleaseSource, store RuntimeInstallationStore, host RuntimeUpdateHost, state domain.RuntimeInstallState, cfg RuntimeUpdateConfig) *RuntimeUpdates {
	if cfg.WaitTimeout <= 0 {
		cfg.WaitTimeout = 10 * time.Minute
	}
	if cfg.CheckInterval <= 0 {
		cfg.CheckInterval = time.Hour
	}
	owned, cancel := context.WithCancel(ctx)
	return &RuntimeUpdates{source: source, store: store, host: host, config: cfg, state: state, phase: state.Phase, lastError: state.LastError, ctx: owned, cancel: cancel}
}

func (s *RuntimeUpdates) Status(context.Context) (domain.RuntimeUpdateStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusLocked(), nil
}

func (s *RuntimeUpdates) statusLocked() domain.RuntimeUpdateStatus {
	current := s.state.Current.Descriptor.Version
	_, stable := domain.ParseRuntimeVersion(current)
	descriptor := s.state.Current.Descriptor
	platform := descriptor.GOOS == "linux" && (descriptor.GOARCH == "amd64" || descriptor.GOARCH == "arm64")
	status := domain.RuntimeUpdateStatus{CurrentVersion: current, LatestVersion: s.latest.Version,
		UpdateAvailable: domain.NewerRuntimeVersion(s.latest.Version, current), CheckedAt: s.checkedAt,
		Source: "github", ReleaseURL: "https://github.com/Dirard/codex-lb/releases", Supported: stable && platform,
		Phase: s.phase, TargetVersion: s.target, LastError: s.lastError}
	if status.Phase == "" {
		status.Phase = "idle"
	}
	if !stable {
		status.UnavailableReason = "Self-update requires a versioned release build"
	} else if !platform {
		status.UnavailableReason = "Self-update releases are available for Linux amd64 and arm64"
	}
	if s.latest.ReleaseURL != "" {
		status.ReleaseURL = s.latest.ReleaseURL
	}
	if previous := s.state.Previous; previous != nil {
		status.PreviousVersion = previous.Descriptor.Version
		status.CanRollback = status.Supported && previous.SHA256 != s.state.Current.SHA256 && compatibleRuntime(descriptor, previous.Descriptor)
	}
	return status
}

func compatibleRuntime(current, target domain.RuntimeDescriptor) bool {
	return current.GOOS == target.GOOS && current.GOARCH == target.GOARCH && current.SchemaVersion == target.SchemaVersion && target.UpdateProtocol == domain.RuntimeUpdateProtocol
}

func (s *RuntimeUpdates) Check(context.Context) (domain.RuntimeUpdateStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.busy {
		if !s.closed && s.phase == "checking" {
			return s.statusLocked(), nil
		}
		return s.statusLocked(), domain.ErrUpdateBusy
	}
	if !s.statusLocked().Supported {
		return s.statusLocked(), domain.ErrUpdateUnavailable
	}
	if time.Now().Before(s.nextCheck) {
		return s.statusLocked(), nil
	}
	s.nextCheck = time.Now().Add(time.Minute)
	s.busy, s.phase, s.lastError = true, "checking", s.state.LastError
	s.jobs.Add(1)
	go func() {
		defer s.jobs.Done()
		ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
		defer cancel()
		release, err := s.source.Latest(ctx)
		s.mu.Lock()
		defer s.mu.Unlock()
		s.busy = false
		if err != nil {
			s.phase, s.lastError = "failed", "Release check failed; try again later"
			if s.state.LastError != "" {
				s.lastError += ". " + s.state.LastError
			}
			return
		}
		now := time.Now().UTC()
		s.checkedAt = &now
		s.latest = release
		s.phase = "idle"
		if s.state.LastError != "" {
			s.phase, s.lastError = "failed", s.state.LastError
		}
	}()
	return s.statusLocked(), nil
}

func (s *RuntimeUpdates) RunChecks(ctx context.Context) {
	_, _ = s.Check(ctx)
	ticker := time.NewTicker(s.config.CheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			_, _ = s.Check(ctx)
		}
	}
}

func (s *RuntimeUpdates) Apply(ctx context.Context, version string) (domain.RuntimeUpdateStatus, error) {
	return s.start(ctx, version, false)
}
func (s *RuntimeUpdates) Rollback(ctx context.Context, version string) (domain.RuntimeUpdateStatus, error) {
	return s.start(ctx, version, true)
}

func (s *RuntimeUpdates) start(_ context.Context, version string, rollback bool) (domain.RuntimeUpdateStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.busy {
		return s.statusLocked(), domain.ErrUpdateBusy
	}
	if !s.statusLocked().Supported {
		return s.statusLocked(), domain.ErrUpdateUnavailable
	}
	var previous *domain.RuntimeInstallation
	if rollback {
		if s.state.Previous == nil || s.state.Previous.Descriptor.Version != version || !s.statusLocked().CanRollback {
			return s.statusLocked(), domain.ErrUpdateTarget
		}
		copy := *s.state.Previous
		previous = &copy
	} else if version != s.latest.Version || !domain.NewerRuntimeVersion(version, s.state.Current.Descriptor.Version) {
		return s.statusLocked(), domain.ErrUpdateTarget
	}
	s.busy, s.target, s.lastError = true, version, ""
	s.phase = "downloading"
	if rollback {
		s.phase = "rolling_back"
	}
	release, original := s.latest, s.state
	s.jobs.Add(1)
	go func() { defer s.jobs.Done(); s.install(release, original, previous) }()
	return s.statusLocked(), nil
}

func (s *RuntimeUpdates) setPhase(phase string) { s.mu.Lock(); s.phase = phase; s.mu.Unlock() }

func (s *RuntimeUpdates) finish(message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.busy = false
	s.lastError = message
	s.phase = "succeeded"
	if message != "" {
		s.phase = "failed"
	}
	s.state.Phase, s.state.LastError = s.phase, message
	if err := s.store.Save(context.Background(), s.state); err != nil {
		s.phase, s.lastError = "failed", "Could not persist runtime update status"
	}
}

func (s *RuntimeUpdates) install(release domain.RuntimeRelease, original domain.RuntimeInstallState, rollback *domain.RuntimeInstallation) {
	ctx := s.ctx
	old := original.Current
	var target domain.RuntimeInstallation
	if rollback != nil {
		target = *rollback
		if s.store.Verify(ctx, target) != nil {
			s.finish("Previous executable is missing or changed")
			return
		}
	} else {
		dir, err := s.store.Stage(ctx)
		if err != nil {
			s.finish("Cannot prepare private update storage")
			return
		}
		defer s.store.DiscardStage(dir)
		binary, err := s.source.Download(ctx, release, dir)
		if err != nil {
			s.finish("Release download or integrity verification failed")
			return
		}
		descriptor, err := s.host.Inspect(ctx, binary.Path)
		if err != nil || descriptor.Version != release.Version || !compatibleRuntime(old.Descriptor, descriptor) {
			s.finish("Release is incompatible with this platform, database schema or update protocol")
			return
		}
		target, err = s.store.Install(ctx, binary, descriptor)
		if err != nil {
			s.finish("Cannot save the verified release")
			return
		}
	}
	checked, err := s.host.Inspect(ctx, target.Path)
	if err != nil || checked != target.Descriptor || !compatibleRuntime(old.Descriptor, checked) {
		s.finish("Target executable is incompatible or changed")
		return
	}
	s.mu.Lock()
	s.state.Pending = &target
	s.state.Phase = "waiting"
	pending := s.state
	s.mu.Unlock()
	if s.store.Save(ctx, pending) != nil {
		s.clearPending()
		s.finish("Cannot persist the pending update")
		return
	}
	s.setPhase("waiting")
	wait, cancel := context.WithTimeout(ctx, s.config.WaitTimeout)
	err = s.host.WaitForIdle(wait)
	cancel()
	if err != nil {
		s.clearPending()
		s.finish("Update deferred: active work did not reach a safe switching window")
		return
	}
	s.setPhase("stopping")
	if err = s.host.Stop(ctx); err != nil {
		s.restore(original, "Previous runtime did not stop cleanly; previous version restored")
		return
	}
	if err = s.host.Backup(ctx); err != nil {
		s.restore(original, "Could not create a consistent backup; previous version restored")
		return
	}
	s.setPhase("starting")
	if s.store.Verify(ctx, target) != nil {
		s.restore(original, "Verified executable changed; previous version restored")
		return
	}
	if err = s.host.PrepareStart(ctx, target); err != nil {
		s.restore(original, "New version failed startup verification; previous version restored")
		return
	}
	s.mu.Lock()
	previousState := s.state
	s.state.Current = target
	s.state.Previous = &old
	s.state.Pending = nil
	// Retain all prior verified versions until activation has been acknowledged.
	s.state.Phase = "starting"
	next := s.state
	s.mu.Unlock()
	if err = s.store.Save(ctx, next); err != nil {
		s.mu.Lock()
		s.state = previousState
		s.mu.Unlock()
		s.restore(original, "Cannot persist the new version; previous version restored")
		return
	}
	if err = s.host.Activate(ctx); err != nil {
		s.restore(original, "New version failed activation; previous version restored")
		return
	}
	s.finish("")
}

func (s *RuntimeUpdates) clearPending() { s.mu.Lock(); s.state.Pending = nil; s.mu.Unlock() }

func (s *RuntimeUpdates) restore(original domain.RuntimeInstallState, message string) {
	if s.ctx.Err() != nil {
		s.finish("Update interrupted; the committed version will be verified on restart")
		return
	}
	s.setPhase("rolling_back")
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), 4*time.Minute)
	defer cancel()
	if err := s.host.Stop(ctx); err != nil {
		s.finish("Failed runtime could not be stopped; manual recovery is required")
		return
	}
	if err := s.store.Verify(ctx, original.Current); err != nil {
		s.finish("Previous executable failed integrity verification; manual recovery is required")
		return
	}
	if err := s.host.PrepareStart(ctx, original.Current); err != nil {
		s.finish("Previous version could not restart; manual recovery is required")
		return
	}
	s.mu.Lock()
	s.state = original
	s.state.Phase = "starting"
	state := s.state
	s.mu.Unlock()
	if err := s.store.Save(ctx, state); err != nil {
		_ = s.host.Stop(ctx)
		s.finish("Cannot persist recovery state; manual recovery is required")
		return
	}
	if err := s.host.Activate(ctx); err != nil {
		s.finish("Previous version could not activate; manual recovery is required")
		return
	}
	s.finish(message)
}

func (s *RuntimeUpdates) Close() error {
	s.mu.Lock()
	s.closed = true
	s.cancel()
	s.mu.Unlock()
	s.jobs.Wait()
	return nil
}

var _ RuntimeUpdater = (*RuntimeUpdates)(nil)
