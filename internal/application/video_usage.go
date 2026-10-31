package application

import (
	"context"
	"errors"
	"sync"
	"time"

	"codex-lb/internal/domain"
)

const (
	videoUsageInterval    = 5 * time.Minute
	videoUsageConcurrency = 4
	// VideoUsageScopeMissing means the account signed in before the pool could
	// be read; a new sign-in grants the needed permission.
	VideoUsageScopeMissing = "scope_missing"
	videoUsageUnavailable  = "unavailable"
)

// Answers of the pool read that are not a reading.
var (
	ErrVideoUsageUnauthorized = errors.New("video usage read was not authorized")
	ErrVideoUsageForbidden    = errors.New("video usage read needs another permission")
)

// VideoUsage is one reading of an account's subscription pool.
type VideoUsage struct {
	Percent   float64
	PeriodEnd time.Time
}

// RefreshUsage reads the pool of every account that can still work, with
// bounded fan-out. A failed read is recorded on its account and keeps the last
// value, so it fails neither the task nor the other accounts.
func (s *VideoService) RefreshUsage(ctx context.Context) error {
	accounts, _, err := s.store.ListVideoAccounts(ctx)
	if err != nil {
		return err
	}
	slots := make(chan struct{}, videoUsageConcurrency)
	var group sync.WaitGroup
	for _, account := range accounts {
		if account.Status != domain.VideoAccountActive && account.Status != domain.VideoAccountPaused {
			continue
		}
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			group.Wait()
			return ctx.Err()
		}
		group.Add(1)
		go func() {
			defer group.Done()
			defer func() { <-slots }()
			s.readAccountUsage(ctx, account.ID)
		}()
	}
	group.Wait()
	return nil
}

// readAccountUsage reads and records one account's pool; never fails its caller.
func (s *VideoService) readAccountUsage(ctx context.Context, accountID string) {
	usage, err := s.fetchAccountUsage(ctx, accountID)
	writeCtx, done := videoWriteContext(ctx)
	defer done()
	now := s.now().UTC()
	if err == nil {
		_ = s.store.SaveVideoUsage(writeCtx, accountID, usage.Percent, usage.PeriodEnd, now)
		return
	}
	if errors.Is(err, context.Canceled) {
		return
	}
	code := videoUsageUnavailable
	if errors.Is(err, ErrVideoUsageForbidden) {
		code = VideoUsageScopeMissing
	}
	_ = s.store.SetVideoUsageError(writeCtx, accountID, code, now)
}

func (s *VideoService) fetchAccountUsage(ctx context.Context, accountID string) (VideoUsage, error) {
	token, version, err := s.accessToken(ctx, accountID, -1)
	if err != nil {
		return VideoUsage{}, err
	}
	usage, err := s.upstream.Usage(ctx, token)
	if errors.Is(err, ErrVideoUsageUnauthorized) {
		if token, _, err = s.accessToken(ctx, accountID, version); err != nil {
			return VideoUsage{}, err
		}
		usage, err = s.upstream.Usage(ctx, token)
	}
	return usage, err
}
