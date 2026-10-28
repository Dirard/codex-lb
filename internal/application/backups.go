package application

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"codex-lb/internal/domain"
)

// BackupDatabase produces verified snapshots of the live database and reports
// its size; the adapter verifies every snapshot with the installation key.
type BackupDatabase interface {
	SnapshotDatabase(ctx context.Context, target string) error
	EstimateSnapshotBytes(ctx context.Context) (int64, error)
	StorageUsage(ctx context.Context) (domain.StorageUsage, error)
}

// BackupSchedules persists the backup schedule.
type BackupSchedules interface {
	LoadBackupSchedule(ctx context.Context) (domain.BackupSchedule, error)
	SaveBackupSchedule(ctx context.Context, schedule domain.BackupSchedule) error
}

// BackupFiles is the private directory that holds published snapshots.
type BackupFiles interface {
	List() ([]domain.BackupFile, error)
	// TempPath returns an unused temporary path and the published name of a
	// snapshot taken at the given time.
	TempPath(at time.Time) (temp, name string, err error)
	Publish(temp, name string) (domain.BackupFile, error)
	Discard(temp string)
	Remove(name string) error
	Open(name string) (io.ReadSeekCloser, domain.BackupFile, error)
	Space() (free, total uint64, err error)
}

// Free-space guard: a snapshot must leave 5 % of the filesystem free, at least
// 256 MiB and at most 2 GiB, so SQLite keeps room for its WAL and temp files.
const (
	backupMinReserveBytes = 256 << 20
	backupMaxReserveBytes = 2 << 30
	backupReserveDivisor  = 20
)

// BackupService owns online backups: guard, single flight, publication,
// rotation and the last-attempt status shown to the administrator.
type BackupService struct {
	db        BackupDatabase
	schedules BackupSchedules
	files     BackupFiles
	now       func() time.Time
	running   sync.Mutex
	mu        sync.Mutex
	active    bool
	last      *domain.BackupAttempt
}

func NewBackupService(db BackupDatabase, schedules BackupSchedules, files BackupFiles, now func() time.Time) *BackupService {
	if now == nil {
		now = time.Now
	}
	return &BackupService{db: db, schedules: schedules, files: files, now: now}
}

// Create runs the backup workflow: free-space guard --> snapshot into a
// temporary file --> verify --> atomic publication --> rotate to the newest N.
// A second call while one is running fails with domain.ErrBackupBusy.
func (s *BackupService) Create(ctx context.Context, trigger string) (domain.BackupFile, error) {
	if !s.running.TryLock() {
		return domain.BackupFile{}, domain.ErrBackupBusy
	}
	defer s.running.Unlock()
	s.setActive(true)
	defer s.setActive(false)
	file, err := s.create(ctx)
	attempt := domain.BackupAttempt{At: s.now().UTC(), Trigger: trigger, Succeeded: err == nil}
	if err == nil {
		attempt.Name = &file.Name
	} else {
		code := backupErrorCode(ctx, err)
		attempt.ErrorCode = &code
	}
	s.mu.Lock()
	s.last = &attempt
	s.mu.Unlock()
	return file, err
}

func (s *BackupService) create(ctx context.Context) (domain.BackupFile, error) {
	schedule, err := s.schedules.LoadBackupSchedule(ctx)
	if err != nil {
		return domain.BackupFile{}, err
	}
	estimate, err := s.db.EstimateSnapshotBytes(ctx)
	if err != nil {
		return domain.BackupFile{}, err
	}
	free, total, err := s.files.Space()
	if err != nil {
		return domain.BackupFile{}, err
	}
	if !BackupSpaceSufficient(estimate, free, total) {
		return domain.BackupFile{}, domain.ErrInsufficientStorage
	}
	temp, name, err := s.files.TempPath(s.now())
	if err != nil {
		return domain.BackupFile{}, err
	}
	if err := s.db.SnapshotDatabase(ctx, temp); err != nil {
		s.files.Discard(temp)
		return domain.BackupFile{}, err
	}
	file, err := s.files.Publish(temp, name)
	if err != nil {
		s.files.Discard(temp)
		return domain.BackupFile{}, err
	}
	// Rotation runs only after a verified publication; it never removes the
	// backup just written.
	return file, s.rotate(schedule.Keep, file.Name)
}

func (s *BackupService) rotate(keep int, newest string) error {
	files, err := s.files.List()
	if err != nil {
		return err
	}
	kept := 0
	var errs []error
	for _, file := range files {
		if file.Name == newest || kept < keep-1 {
			if file.Name != newest {
				kept++
			}
			continue
		}
		errs = append(errs, s.files.Remove(file.Name))
	}
	return errors.Join(errs...)
}

// CreateIfDue is the scheduler step: it starts a scheduled backup when the
// newest backup is older than the saved interval. It reports whether a backup
// was attempted.
func (s *BackupService) CreateIfDue(ctx context.Context) (bool, domain.BackupFile, error) {
	schedule, err := s.schedules.LoadBackupSchedule(ctx)
	if err != nil || schedule.IntervalHours == 0 {
		return false, domain.BackupFile{}, err
	}
	files, err := s.files.List()
	if err != nil {
		return false, domain.BackupFile{}, err
	}
	interval := time.Duration(schedule.IntervalHours) * time.Hour
	if len(files) > 0 && s.now().Sub(files[0].CreatedAt) < interval {
		return false, domain.BackupFile{}, nil
	}
	if last := s.lastAttempt(); last != nil && !last.Succeeded && s.now().Sub(last.At) < time.Hour {
		// A failing backup (for example no disk space) is retried hourly, not on
		// every scheduler tick.
		return false, domain.BackupFile{}, nil
	}
	file, err := s.Create(ctx, domain.BackupTriggerScheduled)
	return true, file, err
}

// Status returns the schedule, published backups, last attempt and storage
// sizes. Snapshots never include the encryption key.
func (s *BackupService) Status(ctx context.Context) (domain.BackupStatus, error) {
	status := domain.BackupStatus{Backups: []domain.BackupFile{}}
	var err error
	if status.Schedule, err = s.schedules.LoadBackupSchedule(ctx); err != nil {
		return status, err
	}
	files, err := s.files.List()
	if err != nil {
		return status, err
	}
	status.Backups = append(status.Backups, files...)
	if status.Storage, err = s.db.StorageUsage(ctx); err != nil {
		return status, err
	}
	for _, file := range files {
		status.Storage.BackupBytes += file.SizeBytes
	}
	if status.Storage.FreeBytes, status.Storage.TotalBytes, err = s.files.Space(); err != nil {
		return status, err
	}
	s.mu.Lock()
	status.Running = s.active
	if s.last != nil {
		last := *s.last
		status.LastAttempt = &last
	}
	s.mu.Unlock()
	return status, nil
}

// UpdateSchedule validates and persists a schedule; it applies without restart.
func (s *BackupService) UpdateSchedule(ctx context.Context, schedule domain.BackupSchedule) (domain.BackupSchedule, error) {
	if err := schedule.Validate(); err != nil {
		return schedule, err
	}
	return schedule, s.schedules.SaveBackupSchedule(ctx, schedule)
}

// Open returns a published backup for download.
func (s *BackupService) Open(name string) (io.ReadSeekCloser, domain.BackupFile, error) {
	return s.files.Open(name)
}

func (s *BackupService) setActive(active bool) {
	s.mu.Lock()
	s.active = active
	s.mu.Unlock()
}

func (s *BackupService) lastAttempt() *domain.BackupAttempt {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

// BackupSpaceSufficient is the free-space guard shared by scheduled, manual
// and CLI backups.
func BackupSpaceSufficient(estimate int64, free, total uint64) bool {
	reserve := min(max(uint64(backupMinReserveBytes), total/backupReserveDivisor), uint64(backupMaxReserveBytes))
	need := uint64(max(estimate, 0)) + reserve
	return free >= need
}

func backupErrorCode(ctx context.Context, err error) string {
	switch {
	case errors.Is(err, domain.ErrInsufficientStorage):
		return "insufficient_space"
	case errors.Is(err, domain.ErrConflict):
		return "backup_exists"
	case ctx.Err() != nil:
		return "cancelled"
	default:
		return "backup_failed"
	}
}
