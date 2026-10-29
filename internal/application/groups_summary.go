package application

import (
	"context"
	"sort"
	"time"

	"codex-lb/internal/domain"
)

// GroupsSummaryRepository is the storage the groups summary reads: the
// overview's entity snapshot with the group limit templates, the overview's
// usage snapshot with the 30-day span, and the recent quota history for burn
// rates.
type GroupsSummaryRepository interface {
	GroupsSummaryEntities(context.Context, time.Time) (domain.GroupsSummaryEntities, error)
	GroupsSummaryUsage(context.Context, domain.GroupsSummaryUsageQuery) (domain.GroupsSummaryUsage, error)
	RecentAccountQuotaHistory(context.Context, time.Time, time.Time) ([]domain.AccountQuota, error)
}

// GroupsSummaryService is use case 7 of the dashboard redesign: every group
// row of the overview with its 30-day spend, limit templates, members and
// member keys by 7-day spend, the pool row and the accounts for the member
// picker.
type GroupsSummaryService struct {
	repo  GroupsSummaryRepository
	shell *ShellStatusService
	now   func() time.Time
}

func NewGroupsSummaryService(repo GroupsSummaryRepository, shell *ShellStatusService, now func() time.Time) *GroupsSummaryService {
	if now == nil {
		now = time.Now
	}
	return &GroupsSummaryService{repo: repo, shell: shell, now: now}
}

// GroupsSummaryUsageQuery is the overview's usage query plus the local
// midnight that starts the 30 local days ending today.
func GroupsSummaryUsageQuery(now time.Time, loc *time.Location) domain.GroupsSummaryUsageQuery {
	local := now.In(loc)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	return domain.GroupsSummaryUsageQuery{Overview: OverviewUsageQuery(now, loc),
		SpanStart: today.AddDate(0, 0, -(domain.GroupsSummaryDays - 1)).UTC()}
}

func (s *GroupsSummaryService) Summary(ctx context.Context, loc *time.Location) (domain.GroupsSummary, error) {
	if loc == nil {
		loc = time.UTC
	}
	now := s.now().UTC()
	entities, err := s.repo.GroupsSummaryEntities(ctx, now)
	if err != nil {
		return domain.GroupsSummary{}, err
	}
	usage, err := s.repo.GroupsSummaryUsage(ctx, GroupsSummaryUsageQuery(now, loc))
	if err != nil {
		return domain.GroupsSummary{}, err
	}
	history, err := s.repo.RecentAccountQuotaHistory(ctx, now.Add(-overviewHistoryWindow), now)
	if err != nil {
		return domain.GroupsSummary{}, err
	}
	shell, err := s.shell.Status(ctx)
	if err != nil {
		return domain.GroupsSummary{}, err
	}
	return buildGroupsSummary(now, loc, shell, entities, usage, history), nil
}

// buildGroupsSummary is the step "extend the overview group rows" of use case
// 7: the overview assembly gives the rows in their order, the span completes
// each key's 30-day spend, and the snapshot gives templates and members.
func buildGroupsSummary(now time.Time, loc *time.Location, shell domain.ShellStatus, entities domain.GroupsSummaryEntities,
	usage domain.GroupsSummaryUsage, history []domain.AccountQuota) domain.GroupsSummary {
	assembly := assembleOverview(now, loc, shell, entities.OverviewEntities, usage.Overview, history)
	known := usage.SpanKnown && usage.Overview.DaysKnown
	spend30 := map[string]float64{}
	if known {
		for _, key := range assembly.keys {
			total := usage.SpanKeys[key.key.ID].CostUSD
			for _, day := range key.usage.Days {
				total += day.CostUSD
			}
			spend30[key.key.ID] = total
		}
	}
	members := map[string][]string{}
	for _, entry := range entities.Accounts {
		for _, group := range entry.GroupIDs {
			members[group] = append(members[group], entry.Account.ID)
		}
	}
	keysOf := map[string][]overviewKey{}
	var ungrouped []overviewKey
	for _, key := range assembly.keys {
		if key.key.GroupID == nil {
			ungrouped = append(ungrouped, key)
			continue
		}
		keysOf[*key.key.GroupID] = append(keysOf[*key.key.GroupID], key)
	}

	summary := domain.GroupsSummary{GeneratedAt: now, Timezone: loc.String(), Shell: shell,
		Groups: make([]domain.GroupSummaryRow, 0, len(assembly.response.Groups))}
	for _, row := range assembly.response.Groups {
		keys, limits, accounts := ungrouped, []domain.LimitTemplate{}, []string{}
		if row.ID != nil {
			keys = keysOf[*row.ID]
			if templates := entities.Limits[*row.ID]; templates != nil {
				limits = templates
			}
			if ids := members[*row.ID]; ids != nil {
				accounts = ids
			}
		}
		summary.Groups = append(summary.Groups, domain.GroupSummaryRow{GroupRow: row, Limits: limits, AccountIDs: accounts,
			Spend30dUSD: keysSpend30(keys, spend30, known), TopKeys7d: groupKeys(keys, spend30, known, now)})
	}
	summary.Pool = poolSummaryRow(assembly, usage, known, now)
	summary.Accounts = groupAccounts(entities.Accounts, assembly.accounts)
	return summary
}

// poolSummaryRow is the group row of every ChatGPT account and every key with
// the pool summary's spend of all reported traffic; its attention comes from
// the alerts that concern the whole pool.
func poolSummaryRow(assembly overviewAssembly, usage domain.GroupsSummaryUsage, known bool, now time.Time) domain.GroupSummaryRow {
	pool := assembly.response.Pool
	row := groupRow(nil, "Pool", assembly.accounts, assembly.keys, usage.Overview.DaysKnown, now)
	row.Primary, row.Secondary = pool.Primary, pool.Secondary
	row.SpendTodayUSD, row.Spend7dUSD, row.Trend7d = pool.SpendTodayUSD, pool.Spend7dUSD, pool.Trend7d
	for _, alert := range assembly.alerts {
		if !alert.Pool {
			continue
		}
		score := 1
		if alert.Severity == domain.SeverityCritical {
			score = 2
		}
		row.Score = max(row.Score, score)
	}
	row.Attention = domain.AttentionLabel(row.Score)
	result := domain.GroupSummaryRow{GroupRow: row, Limits: []domain.LimitTemplate{}, AccountIDs: []string{},
		TopKeys7d: []domain.GroupKey{}}
	if known {
		total := usage.SpanPool.CostUSD
		for _, day := range usage.Overview.Pool.Days {
			total += day.CostUSD
		}
		result.Spend30dUSD = roundPointer(&total, 6)
	}
	return result
}

func keysSpend30(keys []overviewKey, spend30 map[string]float64, known bool) *float64 {
	if !known {
		return nil
	}
	var total float64
	for _, key := range keys {
		total += spend30[key.key.ID]
	}
	return roundPointer(&total, 6)
}

// groupKeys lists at most GroupKeysLimit member keys by 7-day spend.
func groupKeys(keys []overviewKey, spend30 map[string]float64, known bool, now time.Time) []domain.GroupKey {
	out := make([]domain.GroupKey, 0, len(keys))
	for _, key := range keys {
		entry := domain.GroupKey{ID: key.key.ID, Name: key.key.Name, Prefix: key.key.Prefix, Status: key.key.Status(now),
			SpendTodayUSD: key.spendToday, Spend7dUSD: key.spend7d, LastUsedAt: key.key.LastUsedAt}
		if known {
			value := spend30[key.key.ID]
			entry.Spend30dUSD = roundPointer(&value, 6)
		}
		if key.maxLimit != nil {
			entry.MaxLimitPercent = roundPointer(&key.maxLimit.Percent, 2)
		}
		out = append(out, entry)
	}
	sort.SliceStable(out, func(i, j int) bool { return domain.GroupKeyLess(out[i], out[j]) })
	if len(out) > domain.GroupKeysLimit {
		out = out[:domain.GroupKeysLimit]
	}
	return out
}

// groupAccounts lists every non-deleted account with the health the overview
// derived for ChatGPT accounts; external sources have status-only health.
func groupAccounts(entries []domain.OverviewAccount, derived []overviewAccount) []domain.GroupAccount {
	byID := make(map[string]overviewAccount, len(derived))
	for _, account := range derived {
		byID[account.account.ID] = account
	}
	out := make([]domain.GroupAccount, 0, len(entries))
	for _, entry := range entries {
		account := entry.Account
		item := domain.GroupAccount{ID: account.ID, Label: accountLabel(account), Email: account.Email, Kind: account.Kind,
			Status: account.Status, Health: domain.AccountHealthState(account.Status, nil, false)}
		if derived, ok := byID[account.ID]; ok {
			item.Health = derived.health
			item.PrimaryRemainingPercent = roundPointer(derived.primaryRemaining, 2)
			item.SecondaryRemainingPercent = roundPointer(derived.secondaryRemaining, 2)
		}
		out = append(out, item)
	}
	return out
}
