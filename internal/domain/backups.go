package domain

import (
	"errors"
	"time"
)

var (
	// ErrBackupBusy rejects a backup while another one is still running.
	ErrBackupBusy = errors.New("backup already running")
	// ErrInsufficientStorage refuses a snapshot that would starve the
	// database of disk space.
	ErrInsufficientStorage = errors.New("insufficient storage for backup")
)

// Backup schedule bounds; an interval of zero disables scheduled backups.
const (
	BackupMaxIntervalHours     = 168
	BackupMinKeep              = 1
	BackupMaxKeep              = 30
	DefaultBackupIntervalHours = 24
	DefaultBackupKeep          = 3
)

const (
	BackupTriggerScheduled = "scheduled"
	BackupTriggerManual    = "manual"
)

// BackupFile is one published, verified database snapshot. It never contains
// the installation encryption key.
type BackupFile struct {
	Name      string    `json:"name"`
	SizeBytes int64     `json:"sizeBytes"`
	CreatedAt time.Time `json:"createdAt"`
}

type BackupSchedule struct {
	IntervalHours int `json:"intervalHours"`
	Keep          int `json:"keep"`
}

func (s BackupSchedule) Validate() error {
	if s.IntervalHours < 0 || s.IntervalHours > BackupMaxIntervalHours || s.Keep < BackupMinKeep || s.Keep > BackupMaxKeep {
		return ErrInvalid
	}
	return nil
}

// BackupAttempt records the outcome of the latest backup attempt for the
// administrator; ErrorCode is a fixed code, never an underlying error string.
type BackupAttempt struct {
	At        time.Time `json:"at"`
	Trigger   string    `json:"trigger"`
	Succeeded bool      `json:"succeeded"`
	ErrorCode *string   `json:"errorCode"`
	Name      *string   `json:"name"`
}

// StorageUsage describes the live database files and the backup filesystem.
type StorageUsage struct {
	DatabaseBytes int64  `json:"databaseBytes"`
	WALBytes      int64  `json:"walBytes"`
	BackupBytes   int64  `json:"backupBytes"`
	FreeBytes     uint64 `json:"freeBytes"`
	TotalBytes    uint64 `json:"totalBytes"`
}

type BackupStatus struct {
	Schedule              BackupSchedule `json:"schedule"`
	Backups               []BackupFile   `json:"backups"`
	LastAttempt           *BackupAttempt `json:"lastAttempt"`
	Running               bool           `json:"running"`
	EncryptionKeyIncluded bool           `json:"encryptionKeyIncluded"`
	Storage               StorageUsage   `json:"storage"`
}
