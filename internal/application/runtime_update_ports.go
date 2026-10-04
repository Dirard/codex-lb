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

// RuntimeUpdateHost controls only the launcher's own worker. PrepareStart must
// keep public admission and background jobs fenced until Activate succeeds.
type RuntimeUpdateHost interface {
	Inspect(context.Context, string) (domain.RuntimeDescriptor, error)
	WaitForIdle(context.Context) error
	Stop(context.Context) error
	Backup(context.Context) error
	PrepareStart(context.Context, domain.RuntimeInstallation) error
	Activate(context.Context) error
}
