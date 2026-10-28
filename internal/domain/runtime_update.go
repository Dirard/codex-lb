package domain

import (
	"errors"
	"time"
)

// RuntimeUpdateProtocol stays 1: released launchers accept exactly this value.
// Newer abilities are announced by optional descriptor fields instead.
const RuntimeUpdateProtocol = 1

// RuntimeHandoffProtocol is the worker/launcher protocol that lets a new worker
// start beside the running one on the launcher's shared listener.
const RuntimeHandoffProtocol = 1

var (
	ErrUpdateBusy        = errors.New("another runtime update operation is in progress")
	ErrUpdateUnavailable = errors.New("runtime self-update is unavailable")
	ErrUpdateTarget      = errors.New("requested runtime version is not available")
)

// RuntimeUpdateStatus is safe to expose to an authenticated administrator.
// Executable paths, local control credentials and raw subprocess errors stay private.
type RuntimeUpdateStatus struct {
	CurrentVersion    string     `json:"currentVersion"`
	LatestVersion     string     `json:"latestVersion,omitempty"`
	UpdateAvailable   bool       `json:"updateAvailable"`
	CheckedAt         *time.Time `json:"checkedAt"`
	Source            string     `json:"source"`
	ReleaseURL        string     `json:"releaseUrl"`
	Supported         bool       `json:"supported"`
	UnavailableReason string     `json:"unavailableReason,omitempty"`
	// Seamless means the next compatible switch keeps serving without an idle window.
	Seamless        bool   `json:"seamless"`
	PreviousVersion string `json:"previousVersion,omitempty"`
	CanRollback     bool   `json:"canRollback"`
	// RollbackNote explains why a retained previous version cannot be selected.
	RollbackNote  string `json:"rollbackNote,omitempty"`
	Phase         string `json:"phase"`
	TargetVersion string `json:"targetVersion,omitempty"`
	LastError     string `json:"lastError,omitempty"`
}

type RuntimeRelease struct {
	Version     string `json:"version"`
	ArchiveName string `json:"archiveName"`
	ArchiveURL  string `json:"archiveUrl"`
	ChecksumURL string `json:"checksumUrl"`
	ReleaseURL  string `json:"releaseUrl"`
}

// RuntimeDescriptor is returned without opening runtime data by update-info.
// Handoff and SchemaFloor are capabilities: released launchers drop them, so
// descriptors are compared by Identity and capabilities come from inspection.
type RuntimeDescriptor struct {
	Version        string `json:"version"`
	GOOS           string `json:"goos"`
	GOARCH         string `json:"goarch"`
	SchemaVersion  int    `json:"schemaVersion"`
	UpdateProtocol int    `json:"updateProtocol"`
	// Handoff is the RuntimeHandoffProtocol level; zero for older releases.
	Handoff int `json:"handoff,omitempty"`
	// SchemaFloor is the lowest code schema that may operate on a database at
	// SchemaVersion; zero (unknown) means only SchemaVersion itself.
	SchemaFloor int `json:"schemaFloor,omitempty"`
}

// Identity is the descriptor without capability fields.
func (d RuntimeDescriptor) Identity() RuntimeDescriptor {
	d.Handoff, d.SchemaFloor = 0, 0
	return d
}

func (d RuntimeDescriptor) floor() int {
	if d.Handoff < RuntimeHandoffProtocol || d.SchemaFloor <= 0 || d.SchemaFloor > d.SchemaVersion {
		return d.SchemaVersion
	}
	return d.SchemaFloor
}

// SharedSchema reports whether code of both descriptors can operate on the
// database once the newer one has migrated it: the older schema must be at
// least the newer one's floor, and an older binary must be able to read the
// floor the database records.
func SharedSchema(a, b RuntimeDescriptor) bool {
	older, newer := a, b
	if older.SchemaVersion > newer.SchemaVersion {
		older, newer = newer, older
	}
	if older.SchemaVersion == newer.SchemaVersion {
		return true
	}
	return older.Handoff >= RuntimeHandoffProtocol && older.SchemaVersion >= newer.floor()
}

// SeamlessSwitch reports whether target can start beside a running worker of
// current and take over its traffic. The host must also lend that worker the
// shared listener, which the descriptors cannot tell.
func SeamlessSwitch(current, target RuntimeDescriptor) bool {
	return current.GOOS == target.GOOS && current.GOARCH == target.GOARCH && target.UpdateProtocol == RuntimeUpdateProtocol &&
		current.Handoff >= RuntimeHandoffProtocol && target.Handoff >= RuntimeHandoffProtocol && SharedSchema(current, target)
}

type RuntimeBinary struct {
	Path   string
	SHA256 string
}

// RuntimeInstallation is private persisted metadata, never an administrator payload.
type RuntimeInstallation struct {
	Descriptor RuntimeDescriptor `json:"descriptor"`
	Path       string            `json:"-"`
	SHA256     string            `json:"sha256"`
}

type RuntimeInstallState struct {
	Current         RuntimeInstallation  `json:"current"`
	Previous        *RuntimeInstallation `json:"previous,omitempty"`
	Pending         *RuntimeInstallation `json:"pending,omitempty"`
	Phase           string               `json:"phase"`
	LastError       string               `json:"lastError,omitempty"`
	BootstrapSHA256 string               `json:"bootstrapSha256,omitempty"`
}
