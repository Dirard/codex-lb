package domain

import (
	"errors"
	"time"
)

const RuntimeUpdateProtocol = 1

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
	PreviousVersion   string     `json:"previousVersion,omitempty"`
	CanRollback       bool       `json:"canRollback"`
	Phase             string     `json:"phase"`
	TargetVersion     string     `json:"targetVersion,omitempty"`
	LastError         string     `json:"lastError,omitempty"`
}

type RuntimeRelease struct {
	Version     string `json:"version"`
	ArchiveName string `json:"archiveName"`
	ArchiveURL  string `json:"archiveUrl"`
	ChecksumURL string `json:"checksumUrl"`
	ReleaseURL  string `json:"releaseUrl"`
}

// RuntimeDescriptor is returned without opening runtime data by update-info.
type RuntimeDescriptor struct {
	Version        string `json:"version"`
	GOOS           string `json:"goos"`
	GOARCH         string `json:"goarch"`
	SchemaVersion  int    `json:"schemaVersion"`
	UpdateProtocol int    `json:"updateProtocol"`
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
