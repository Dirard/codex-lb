package application

import (
	"context"

	"codex-lb/internal/domain"
)

// RuntimeUpdater is the administrator boundary to the owned update controller.
// Check/Apply/Rollback acknowledge owned asynchronous work rather than tying it
// to the lifetime of the administrator's HTTP connection.
type RuntimeUpdater interface {
	Status(context.Context) (domain.RuntimeUpdateStatus, error)
	Check(context.Context) (domain.RuntimeUpdateStatus, error)
	Apply(context.Context, string) (domain.RuntimeUpdateStatus, error)
	Rollback(context.Context, string) (domain.RuntimeUpdateStatus, error)
}

// RuntimeReleaseSource validates its fixed release origin and archive integrity.
// Download writes only into an empty private staging directory supplied by its caller.
type RuntimeReleaseSource interface {
	Latest(context.Context) (domain.RuntimeRelease, error)
	Download(context.Context, domain.RuntimeRelease, string) (domain.RuntimeBinary, error)
}

type RuntimeInstallationStore interface {
	Save(context.Context, domain.RuntimeInstallState) error
	Stage(context.Context) (string, error)
	Install(context.Context, domain.RuntimeBinary, domain.RuntimeDescriptor) (domain.RuntimeInstallation, error)
	Verify(context.Context, domain.RuntimeInstallation) error
	DiscardStage(string)
}

// RuntimeUpdateHost controls only the launcher's own workers. PrepareStart must
// keep public admission and background jobs fenced until Activate succeeds.
type RuntimeUpdateHost interface {
	Inspect(context.Context, string) (domain.RuntimeDescriptor, error)
	WaitForIdle(context.Context) error
	Stop(context.Context) error
	// Backup takes the pre-switch snapshot: online while the host lends the
	// running worker its database lock, otherwise after Stop.
	Backup(context.Context) error
	// Overlap reports whether the running worker shares the launcher's
	// listener, so a handoff-capable candidate can start beside it.
	Overlap() bool
	// StartBeside starts a candidate next to the running worker with its
	// background work deferred and returns once the candidate is ready.
	StartBeside(context.Context, domain.RuntimeInstallation) error
	// Handover activates the candidate, retires the running worker and moves
	// background work to the candidate. On error the candidate has been stopped
	// and the running worker keeps serving.
	Handover(context.Context) error
	// DiscardCandidate stops a candidate that was never handed traffic.
	DiscardCandidate(context.Context) error
	// RestoreBackup replaces the stopped runtime's database with the snapshot
	// taken by the last Backup, discarding a migration by a failed newer version.
	RestoreBackup(context.Context) error
	PrepareStart(context.Context, domain.RuntimeInstallation) error
	Activate(context.Context) error
}
