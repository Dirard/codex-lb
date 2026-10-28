package application

import (
	"context"
	"errors"
	"io"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

type fakeBackupDatabase struct {
	estimate int64
	fail     error
	block    chan struct{}
	started  chan struct{}
	snapped  []string
}

func (f *fakeBackupDatabase) SnapshotDatabase(_ context.Context, target string) error {
	if f.started != nil {
		close(f.started)
	}
	if f.block != nil {
		<-f.block
	}
	f.snapped = append(f.snapped, target)
	return f.fail
}
func (f *fakeBackupDatabase) EstimateSnapshotBytes(context.Context) (int64, error) {
	return f.estimate, nil
}
func (f *fakeBackupDatabase) StorageUsage(context.Context) (domain.StorageUsage, error) {
	return domain.StorageUsage{DatabaseBytes: 10, WALBytes: 2}, nil
}

type fakeBackupSchedules struct{ schedule domain.BackupSchedule }

func (f *fakeBackupSchedules) LoadBackupSchedule(context.Context) (domain.BackupSchedule, error) {
	return f.schedule, nil
}
func (f *fakeBackupSchedules) SaveBackupSchedule(_ context.Context, schedule domain.BackupSchedule) error {
	f.schedule = schedule
	return nil
}

type fakeBackupFiles struct {
	mu        sync.Mutex
	files     map[string]domain.BackupFile
	discarded []string
	free      uint64
	total     uint64
}

func (f *fakeBackupFiles) List() ([]domain.BackupFile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := []domain.BackupFile{}
	for _, file := range f.files {
		result = append(result, file)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name > result[j].Name })
	return result, nil
}
func (f *fakeBackupFiles) TempPath(at time.Time) (string, string, error) {
	name := "codex-lb-" + at.UTC().Format("20060102T150405Z") + ".sqlite3"
	return "/backups/." + name + ".tmp", name, nil
}
func (f *fakeBackupFiles) Publish(temp, name string) (domain.BackupFile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.files[name]; ok {
		return domain.BackupFile{}, domain.ErrConflict
	}
	at, _ := time.Parse("20060102T150405Z", strings.TrimSuffix(strings.TrimPrefix(name, "codex-lb-"), ".sqlite3"))
	file := domain.BackupFile{Name: name, SizeBytes: 7, CreatedAt: at}
	f.files[name] = file
	return file, nil
}
func (f *fakeBackupFiles) Discard(temp string) { f.discarded = append(f.discarded, temp) }
func (f *fakeBackupFiles) Remove(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.files, name)
	return nil
}
func (f *fakeBackupFiles) Open(string) (io.ReadSeekCloser, domain.BackupFile, error) {
	return nil, domain.BackupFile{}, domain.ErrNotFound
}
func (f *fakeBackupFiles) Space() (uint64, uint64, error) { return f.free, f.total, nil }

func newBackupFixture(now *time.Time) (*BackupService, *fakeBackupDatabase, *fakeBackupSchedules, *fakeBackupFiles) {
	db := &fakeBackupDatabase{estimate: 1 << 20}
	schedules := &fakeBackupSchedules{schedule: domain.BackupSchedule{IntervalHours: 24, Keep: 3}}
	files := &fakeBackupFiles{files: map[string]domain.BackupFile{}, free: 100 << 30, total: 200 << 30}
	return NewBackupService(db, schedules, files, func() time.Time { return *now }), db, schedules, files
}

func TestBackupCreateRotatesOnlyAfterSuccess(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC)
	service, db, _, files := newBackupFixture(&now)
	for i := range 4 {
		at := now.Add(time.Duration(i-4) * 24 * time.Hour)
		name := "codex-lb-" + at.Format("20060102T150405Z") + ".sqlite3"
		files.files[name] = domain.BackupFile{Name: name, CreatedAt: at}
	}
	db.fail = errors.New("verification failed")
	if _, err := service.Create(ctx, domain.BackupTriggerManual); err == nil {
		t.Fatal("failed snapshot reported success")
	}
	if len(files.files) != 4 || len(files.discarded) != 1 {
		t.Fatalf("failed attempt changed backups: files=%d discarded=%v", len(files.files), files.discarded)
	}
	status, err := service.Status(ctx)
	if err != nil || status.LastAttempt == nil || status.LastAttempt.Succeeded || *status.LastAttempt.ErrorCode != "backup_failed" {
		t.Fatalf("status after failure = %+v %v", status.LastAttempt, err)
	}
	db.fail = nil
	file, err := service.Create(ctx, domain.BackupTriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	listed, _ := files.List()
	if len(listed) != 3 || listed[0].Name != file.Name || listed[2].Name != "codex-lb-20261026T030000Z.sqlite3" {
		t.Fatalf("rotation kept %+v", listed)
	}
	status, err = service.Status(ctx)
	if err != nil || !status.LastAttempt.Succeeded || *status.LastAttempt.Name != file.Name || status.EncryptionKeyIncluded ||
		status.Storage.BackupBytes != 7 || status.Storage.FreeBytes != 100<<30 {
		t.Fatalf("status = %+v %v", status, err)
	}
}

func TestBackupRefusesLowSpaceAndConcurrentRuns(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC)
	service, db, _, files := newBackupFixture(&now)
	// 40 GiB disk: reserve 2 GiB; a 1 GiB snapshot needs 3 GiB free.
	files.total, files.free, db.estimate = 40<<30, 3<<30-1, 1<<30
	if _, err := service.Create(ctx, domain.BackupTriggerManual); !errors.Is(err, domain.ErrInsufficientStorage) {
		t.Fatalf("low space: %v", err)
	}
	if len(db.snapped) != 0 {
		t.Fatal("snapshot written despite the guard")
	}
	files.free = 3 << 30
	db.block, db.started = make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := service.Create(ctx, domain.BackupTriggerScheduled)
		done <- err
	}()
	<-db.started
	if _, err := service.Create(ctx, domain.BackupTriggerManual); !errors.Is(err, domain.ErrBackupBusy) {
		t.Fatalf("concurrent backup: %v", err)
	}
	if status, _ := service.Status(ctx); !status.Running {
		t.Fatal("running backup not reported")
	}
	close(db.block)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		estimate    int64
		free, total uint64
		sufficient  bool
		description string
	}{
		{0, 256 << 20, 1 << 30, true, "small disk keeps 256 MiB"},
		{0, 256<<20 - 1, 1 << 30, false, "below the minimum reserve"},
		{1 << 30, 3 << 30, 2 << 40, true, "large disk reserve capped at 2 GiB"},
	} {
		if BackupSpaceSufficient(tc.estimate, tc.free, tc.total) != tc.sufficient {
			t.Fatal(tc.description)
		}
	}
}

func TestBackupScheduleDueDisabledAndRetry(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC)
	service, db, schedules, files := newBackupFixture(&now)
	if attempted, _, err := service.CreateIfDue(ctx); !attempted || err != nil {
		t.Fatalf("first scheduled backup: %v %v", attempted, err)
	}
	now = now.Add(23 * time.Hour)
	if attempted, _, _ := service.CreateIfDue(ctx); attempted {
		t.Fatal("backup before the interval elapsed")
	}
	now = now.Add(time.Hour)
	db.fail = errors.New("disk error")
	if attempted, _, err := service.CreateIfDue(ctx); !attempted || err == nil {
		t.Fatalf("due backup: %v %v", attempted, err)
	}
	now = now.Add(10 * time.Minute)
	if attempted, _, _ := service.CreateIfDue(ctx); attempted {
		t.Fatal("failed backup retried before an hour")
	}
	now = now.Add(time.Hour)
	db.fail = nil
	if attempted, _, err := service.CreateIfDue(ctx); !attempted || err != nil {
		t.Fatalf("retry: %v %v", attempted, err)
	}
	if _, err := service.UpdateSchedule(ctx, domain.BackupSchedule{IntervalHours: 0, Keep: 3}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(48 * time.Hour)
	if attempted, _, _ := service.CreateIfDue(ctx); attempted || schedules.schedule.IntervalHours != 0 {
		t.Fatal("disabled schedule created a backup")
	}
	if len(files.files) != 2 {
		t.Fatalf("disabled schedule changed backups: %d", len(files.files))
	}
	if _, err := service.UpdateSchedule(ctx, domain.BackupSchedule{IntervalHours: 200, Keep: 3}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("invalid schedule: %v", err)
	}
}
