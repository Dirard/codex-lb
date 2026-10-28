package application

import (
	"context"
	"time"

	"codex-lb/internal/domain"
)

// ShellStatusRepository is the storage the dashboard shell status reads: one
// snapshot of accounts, the newest quota observation and the rollup state,
// the settings and the readiness probe of /health/ready.
type ShellStatusRepository interface {
	ShellSnapshot(context.Context) (domain.ShellSnapshot, error)
	LoadSettings(context.Context) (domain.RuntimeSettings, error)
	Health(context.Context) error
}

// ResetCreditCounter sums the in-memory reset-credit snapshot of accounts.
type ResetCreditCounter interface {
	AvailableResetCredits([]domain.Account) int
}

// ShellStatusService computes the status every administrator page shows in
// its header and status bar from a fixed number of reads.
type ShellStatusService struct {
	repo    ShellStatusRepository
	credits ResetCreditCounter
}

// NewShellStatusService builds the use case; credits may be nil, which
// reports no reset credits.
func NewShellStatusService(repo ShellStatusRepository, credits ResetCreditCounter) *ShellStatusService {
	return &ShellStatusService{repo: repo, credits: credits}
}

// shellReadinessTimeout matches the /health/ready probe.
const shellReadinessTimeout = time.Second

func (s *ShellStatusService) Status(ctx context.Context) (domain.ShellStatus, error) {
	var status domain.ShellStatus
	snapshot, err := s.repo.ShellSnapshot(ctx)
	if err != nil {
		return status, err
	}
	settings, err := s.repo.LoadSettings(ctx)
	if err != nil {
		return status, err
	}
	probe, cancel := context.WithTimeout(ctx, shellReadinessTimeout)
	status.Ready = s.repo.Health(probe) == nil
	cancel()
	status.LastSyncAt, status.UsageFoldedAt, status.RollupsUsable = snapshot.LastSyncAt, snapshot.UsageFoldedAt, snapshot.RollupsUsable
	for _, account := range snapshot.Accounts {
		if account.Kind == domain.AccountChatGPT && account.Status == domain.AccountReauthRequired {
			status.ReauthRequiredAccounts++
		}
	}
	if s.credits != nil {
		status.AvailableResetCredits = s.credits.AvailableResetCredits(snapshot.Accounts)
	}
	status.ShowResetCreditBadges = settings.ShowResetCreditBadges
	status.APIKeyAuthEnabled = settings.APIKeyAuthEnabled
	status.RoutingStrategy = settings.RoutingStrategy
	status.StickyThreadsEnabled = settings.StickyThreadsEnabled
	status.PreferEarlierResetAccounts = settings.PreferEarlierResetAccounts
	status.PreferEarlierResetWindow = settings.PreferEarlierResetWindow
	return status, nil
}

// AvailableResetCredits sums the reset credits of the in-memory snapshot for
// accounts the usage poller serves, without storage or provider reads.
func (s *AccountUsageService) AvailableResetCredits(accounts []domain.Account) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	total := 0
	for _, account := range accounts {
		if !usageEligible(account) {
			continue
		}
		if snapshot, ok := s.snapshots[accountGenerationKey{accountID: account.ID, generation: account.Generation}]; ok {
			total += max(0, snapshot.AvailableCount)
		}
	}
	return total
}

// DirectoryRepository lists the entities the dashboard search names.
type DirectoryRepository interface {
	Directory(context.Context, time.Time) (domain.Directory, error)
}

// DirectoryService serves the global search and the entity pickers.
type DirectoryService struct {
	repo DirectoryRepository
	now  func() time.Time
}

func NewDirectoryService(repo DirectoryRepository, now func() time.Time) *DirectoryService {
	if now == nil {
		now = time.Now
	}
	return &DirectoryService{repo: repo, now: now}
}

func (s *DirectoryService) Directory(ctx context.Context) (domain.Directory, error) {
	return s.repo.Directory(ctx, s.now().UTC())
}
