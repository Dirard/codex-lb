package application

import (
	"context"
	"sort"
	"time"

	"codex-lb/internal/domain"
)

// OverviewRepository is the storage the overview reads: one snapshot of the
// accounts, groups and keys, one analytics snapshot of usage and the recent
// quota history for burn rates.
type OverviewRepository interface {
	OverviewEntities(context.Context, time.Time) (domain.OverviewEntities, error)
	OverviewUsage(context.Context, domain.OverviewUsageQuery) (domain.OverviewUsage, error)
	RecentAccountQuotaHistory(context.Context, time.Time, time.Time) ([]domain.AccountQuota, error)
}

// OverviewService is use case 2 of the dashboard redesign: the pool, the
// account groups with the ungrouped keys, the alerts and the keys needing
// attention, for the local day of a time zone.
type OverviewService struct {
	repo  OverviewRepository
	shell *ShellStatusService
	now   func() time.Time
}

func NewOverviewService(repo OverviewRepository, shell *ShellStatusService, now func() time.Time) *OverviewService {
	if now == nil {
		now = time.Now
	}
	return &OverviewService{repo: repo, shell: shell, now: now}
}

// overviewDays is today and the 7 local days before it: the 7-day trend ends
// today and a spend spike compares today with the 7 days before.
const overviewDays = domain.OverviewSpendSpikeHistory + 1

// overviewHistoryWindow bounds the quota history read for burn rates.
const overviewHistoryWindow = 6 * time.Hour

// OverviewUsageQuery is the step "local day bounds" of use case 2: local
// midnights of the 8 days ending today, the same local time yesterday, and
// hour-aligned error windows: the last hour is the previous whole hour plus
// the current partial hour, the trailing window the 24 hours before it.
func OverviewUsageQuery(now time.Time, loc *time.Location) domain.OverviewUsageQuery {
	local := now.In(loc)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	days := make([]time.Time, overviewDays)
	for i := range days {
		days[i] = today.AddDate(0, 0, i-(overviewDays-1)).UTC()
	}
	yesterday := local.AddDate(0, 0, -1).UTC()
	if yesterday.Before(days[overviewDays-2]) {
		yesterday = days[overviewDays-2]
	}
	if yesterday.After(days[overviewDays-1]) {
		yesterday = days[overviewDays-1]
	}
	lastHour := now.UTC().Truncate(time.Hour).Add(-time.Hour)
	return domain.OverviewUsageQuery{DayStarts: days, Now: now.UTC(), YesterdaySameTime: yesterday,
		LastHourStart: lastHour, TrailingStart: lastHour.Add(-24 * time.Hour)}
}

// overviewAccount is one ChatGPT account with its derived health and windows.
type overviewAccount struct {
	account            domain.Account
	label              string
	health             domain.AccountHealth
	primary, secondary *domain.WindowAccount
	primaryRemaining   *float64
	primaryReset       *time.Time
	secondaryRemaining *float64
	secondaryReset     *time.Time
	groups             []string
}

// overviewKey is one key with its usage.
type overviewKey struct {
	key        domain.OverviewKey
	active     bool
	maxLimit   *domain.LimitState
	usage      domain.OverviewScopeUsage
	spendToday float64
	spend7d    *float64
	trend      []*float64
	previous   []float64
}

func (s *OverviewService) Overview(ctx context.Context, loc *time.Location) (domain.OverviewResponse, error) {
	if loc == nil {
		loc = time.UTC
	}
	now := s.now().UTC()
	entities, err := s.repo.OverviewEntities(ctx, now)
	if err != nil {
		return domain.OverviewResponse{}, err
	}
	usage, err := s.repo.OverviewUsage(ctx, OverviewUsageQuery(now, loc))
	if err != nil {
		return domain.OverviewResponse{}, err
	}
	history, err := s.repo.RecentAccountQuotaHistory(ctx, now.Add(-overviewHistoryWindow), now)
	if err != nil {
		return domain.OverviewResponse{}, err
	}
	shell, err := s.shell.Status(ctx)
	if err != nil {
		return domain.OverviewResponse{}, err
	}
	return buildOverview(now, loc, shell, entities, usage, history), nil
}

// buildOverview is the assembly step of use case 2: account health and
// windows, key usage, group rows, alerts, group attention and ordering, and
// the attention keys.
func buildOverview(now time.Time, loc *time.Location, shell domain.ShellStatus, entities domain.OverviewEntities,
	usage domain.OverviewUsage, history []domain.AccountQuota) domain.OverviewResponse {
	series := map[string]map[string][]domain.AccountQuota{}
	for _, row := range history {
		if series[row.AccountID] == nil {
			series[row.AccountID] = map[string][]domain.AccountQuota{}
		}
		series[row.AccountID][row.Window] = append(series[row.AccountID][row.Window], row)
	}
	response := domain.OverviewResponse{GeneratedAt: now, Timezone: loc.String(), Shell: shell}
	var accounts []overviewAccount
	polled := 0
	for _, entry := range entities.Accounts {
		if entry.Account.Kind != domain.AccountChatGPT {
			response.Pool.ExternalSources++
			continue
		}
		switch entry.Account.Status {
		case domain.AccountActive, domain.AccountRateLimited, domain.AccountQuotaExceeded:
			polled++
		}
		accounts = append(accounts, overviewAccountOf(entry, series[entry.Account.ID], now))
	}
	keys := make([]overviewKey, 0, len(entities.Keys))
	for _, key := range entities.Keys {
		keys = append(keys, overviewKeyOf(key, usage, now))
	}

	poolRow := groupRow(nil, "Ungrouped keys", accounts, keysWhere(keys, func(k overviewKey) bool { return k.key.GroupID == nil }), usage.DaysKnown, now)
	response.Pool = poolSummary(response.Pool.ExternalSources, accounts, usage, now)
	poolRow.Primary, poolRow.Secondary = response.Pool.Primary, response.Pool.Secondary
	groups := make([]domain.GroupRow, 0, len(entities.Groups)+1)
	runways := []domain.AlertRunwaySubject{{Name: "pool", Windows: []domain.AlertWindow{
		{Key: "5h", Health: response.Pool.Primary}, {Key: "weekly", Health: response.Pool.Secondary}}}}
	for _, ref := range entities.Groups {
		members := make([]overviewAccount, 0)
		for _, account := range accounts {
			if contains(account.groups, ref.ID) {
				members = append(members, account)
			}
		}
		id := ref.ID
		row := groupRow(&id, ref.Name, members, keysWhere(keys, func(k overviewKey) bool { return k.key.GroupID != nil && *k.key.GroupID == id }), usage.DaysKnown, now)
		groups = append(groups, row)
		runways = append(runways, domain.AlertRunwaySubject{ID: &id, Name: ref.Name, Windows: []domain.AlertWindow{
			{Key: "5h", Health: row.Primary}, {Key: "weekly", Health: row.Secondary}}})
	}

	inputs := domain.AlertInputs{Now: now, Runways: runways, PoolLastHour: usage.Pool.LastHour, PoolTrailing: usage.Pool.Trailing,
		ErrorsKnown: true, LastQuotaAt: shell.LastSyncAt, PolledChatGPTCount: polled}
	for _, account := range accounts {
		if account.health == domain.HealthReauth {
			inputs.ReauthAccounts = append(inputs.ReauthAccounts, domain.AlertAccount{ID: account.account.ID, Label: account.label, GroupIDs: account.groups})
		}
	}
	groupNames := map[string]string{}
	for _, ref := range entities.Groups {
		groupNames[ref.ID] = ref.Name
	}
	var attention []domain.AttentionKey
	for _, key := range keys {
		if !key.active {
			continue
		}
		inputs.Keys = append(inputs.Keys, domain.AlertKey{ID: key.key.ID, Name: key.key.Name, GroupID: key.key.GroupID,
			MaxLimit: key.maxLimit, ExpiresAt: key.key.ExpiresAt, LastHour: key.usage.LastHour, LastHourKnown: true})
		reasons := domain.KeyAttentionReasons(domain.AttentionSignals{MaxLimit: key.maxLimit, ExpiresAt: key.key.ExpiresAt,
			LastHour: key.usage.LastHour, LastHourKnown: true, SpendToday: key.spendToday, PreviousDays: key.previous}, now)
		if len(reasons) == 0 {
			continue
		}
		entry := domain.AttentionKey{ID: key.key.ID, Name: key.key.Name, Prefix: key.key.Prefix, GroupID: key.key.GroupID,
			Reasons: reasons, SpendTodayUSD: key.spendToday, Spend7dUSD: key.spend7d, MaxLimit: key.maxLimit,
			ExpiresAt: key.key.ExpiresAt, LastUsedAt: key.key.LastUsedAt,
			PrimaryAction: domain.PrimaryAttentionAction(reasons[0].Reason, key.key.GroupID != nil)}
		if key.key.GroupID != nil {
			name := groupNames[*key.key.GroupID]
			entry.GroupName = &name
		}
		attention = append(attention, entry)
	}
	raw := domain.RawAlerts(inputs)
	response.Alerts = domain.CollapseAlerts(raw)
	scores := map[string]int{}
	ungrouped := 0
	for _, alert := range raw {
		score := 1
		if alert.Severity == domain.SeverityCritical {
			score = 2
		}
		if alert.Group != nil {
			scores[*alert.Group] = max(scores[*alert.Group], score)
		}
		if alert.Pool || alert.Group == nil {
			ungrouped = max(ungrouped, score)
		}
	}
	for i := range groups {
		groups[i].Score = scores[*groups[i].ID]
		groups[i].Attention = domain.AttentionLabel(groups[i].Score)
	}
	poolRow.Score, poolRow.Attention = ungrouped, domain.AttentionLabel(ungrouped)
	groups = append(groups, poolRow)
	sort.SliceStable(groups, func(i, j int) bool { return domain.GroupRowLess(groups[i], groups[j]) })
	response.Groups = groups
	response.AttentionKeys, response.AttentionKeysTotal = domain.RankAttentionKeys(attention, domain.AttentionKeysLimit)
	return response
}

func overviewAccountOf(entry domain.OverviewAccount, series map[string][]domain.AccountQuota, now time.Time) overviewAccount {
	account := entry.Account
	result := overviewAccount{account: account, label: accountLabel(account), groups: entry.GroupIDs}
	status := EffectiveAccountQuotaStatus(account, entry.Quotas, entry.Credits, entry.RefusalAt, now)
	primary, long := selectionQuotaWindows(account, entry.Quotas)
	var remaining []float64
	window := func(quota *domain.AccountQuota, capacityKey string, length time.Duration) (*domain.WindowAccount, *float64, *time.Time) {
		if quota == nil {
			return nil, nil, nil
		}
		used, reset := quotaSelectionValue(quota.UsedPercent, quota.ResetAt, now)
		left := max(0, min(100, 100-used))
		remaining = append(remaining, left)
		if reset == nil && quota.ResetAt != nil {
			// The observed window has reset since: no current observation.
			return nil, &left, nil
		}
		capacity := domain.SubscriptionCreditCapacity(account.PlanType, capacityKey)
		if capacity == nil || *capacity <= 0 {
			return nil, &left, reset
		}
		if quota.WindowMinutes != nil && *quota.WindowMinutes > 0 {
			length = time.Duration(*quota.WindowMinutes) * time.Minute
		}
		return &domain.WindowAccount{Capacity: *capacity, Remaining: *capacity * left / 100, ResetAt: reset,
			Burn: domain.QuotaBurnPerHour(series[quota.Window], *capacity, now), Length: length}, &left, reset
	}
	result.primary, result.primaryRemaining, result.primaryReset = window(primary, "primary", 5*time.Hour)
	longKey := "secondary"
	if long != nil && long.Window == "monthly" {
		longKey = "monthly"
	}
	result.secondary, result.secondaryRemaining, result.secondaryReset = window(long, longKey, 7*24*time.Hour)
	result.health = domain.AccountHealthState(status, remaining, entry.Credits != nil && entry.Credits.Usable())
	return result
}

func accountLabel(account domain.Account) string {
	switch {
	case account.Alias != "":
		return account.Alias
	case account.Email != "":
		return account.Email
	}
	return account.ID
}

func overviewKeyOf(key domain.OverviewKey, usage domain.OverviewUsage, now time.Time) overviewKey {
	entry := overviewKey{key: key, active: key.Status(now) == "active", maxLimit: domain.FullestLimit(key.Limits)}
	scope, ok := usage.Keys[key.ID]
	if !ok {
		scope = domain.OverviewScopeUsage{Days: make([]domain.UsageDay, len(usage.Pool.Days))}
	}
	entry.usage = scope
	days := scope.Days
	if len(days) > 0 {
		entry.spendToday = roundTo(days[len(days)-1].CostUSD, 6)
	}
	if usage.DaysKnown && len(days) == overviewDays {
		var sum float64
		for _, day := range days[len(days)-domain.OverviewTrendDays:] {
			cost := roundTo(day.CostUSD, 6)
			entry.trend = append(entry.trend, &cost)
			sum += day.CostUSD
		}
		total := roundTo(sum, 6)
		entry.spend7d = &total
		for _, day := range days[:domain.OverviewSpendSpikeHistory] {
			entry.previous = append(entry.previous, day.CostUSD)
		}
	}
	return entry
}

func keysWhere(keys []overviewKey, keep func(overviewKey) bool) []overviewKey {
	var out []overviewKey
	for _, key := range keys {
		if keep(key) {
			out = append(out, key)
		}
	}
	return out
}

// windowHealth aggregates the covered accounts of one window.
func windowHealth(accounts []overviewAccount, pick func(overviewAccount) *domain.WindowAccount, length time.Duration, now time.Time) domain.WindowHealth {
	var covered []domain.WindowAccount
	for _, account := range accounts {
		if window := pick(account); window != nil {
			covered = append(covered, *window)
		}
	}
	health := domain.AggregateWindow(covered, len(accounts), length, now)
	health.RemainingPercent = roundPointer(health.RemainingPercent, 2)
	health.RemainingCredits = roundPointer(health.RemainingCredits, 2)
	health.CapacityCredits = roundPointer(health.CapacityCredits, 2)
	health.BurnCreditsPerHour = roundPointer(health.BurnCreditsPerHour, 2)
	return health
}

func roundPointer(value *float64, places int) *float64 {
	if value == nil {
		return nil
	}
	rounded := roundTo(*value, places)
	return &rounded
}

func primaryWindow(a overviewAccount) *domain.WindowAccount   { return a.primary }
func secondaryWindow(a overviewAccount) *domain.WindowAccount { return a.secondary }

// groupRow builds one row over its member accounts and keys; limit fill and
// key counts consider active keys, spend every member key.
func groupRow(id *string, name string, accounts []overviewAccount, keys []overviewKey, daysKnown bool, now time.Time) domain.GroupRow {
	row := domain.GroupRow{ID: id, Name: name, ProblemAccounts: []domain.ProblemAccount{}, TopKeys: []domain.TopKey{},
		Primary:   windowHealth(accounts, primaryWindow, 5*time.Hour, now),
		Secondary: windowHealth(accounts, secondaryWindow, 7*24*time.Hour, now)}
	for _, account := range accounts {
		row.Accounts.Add(account.health)
		if account.health == domain.HealthOK {
			continue
		}
		action := "open"
		switch account.health {
		case domain.HealthReauth:
			action = "reauth"
		case domain.HealthPaused:
			action = "resume"
		}
		row.ProblemAccounts = append(row.ProblemAccounts, domain.ProblemAccount{ID: account.account.ID, Label: account.label,
			Status: account.account.Status, Health: account.health,
			PrimaryRemainingPercent: roundPointer(account.primaryRemaining, 2), PrimaryResetAt: account.primaryReset,
			SecondaryRemainingPercent: roundPointer(account.secondaryRemaining, 2), SecondaryResetAt: account.secondaryReset, Action: action})
	}
	sort.SliceStable(row.ProblemAccounts, func(i, j int) bool {
		return domain.ProblemAccountLess(row.ProblemAccounts[i], row.ProblemAccounts[j])
	})
	if len(row.ProblemAccounts) > domain.ProblemAccountsLimit {
		row.ProblemAccounts = row.ProblemAccounts[:domain.ProblemAccountsLimit]
	}
	var spend7d float64
	trend := make([]float64, domain.OverviewTrendDays)
	for _, key := range keys {
		row.Keys.Total++
		row.SpendTodayUSD += key.spendToday
		if key.spend7d != nil {
			spend7d += *key.spend7d
			for i, cost := range key.trend {
				trend[i] += *cost
			}
		}
		if !key.active {
			continue
		}
		row.Keys.Active++
		if key.maxLimit != nil {
			percent := roundTo(key.maxLimit.Percent, 2)
			if row.LimitFill.MaxPercent == nil || percent > *row.LimitFill.MaxPercent {
				row.LimitFill.MaxPercent = &percent
			}
			if key.maxLimit.Percent >= domain.KeyLimitWarningPercent {
				row.LimitFill.KeysAtOrAbove90++
			}
			if key.maxLimit.Percent >= domain.KeyLimitCriticalPercent {
				row.LimitFill.KeysBlocked++
			}
		}
	}
	row.SpendTodayUSD = roundTo(row.SpendTodayUSD, 6)
	if daysKnown {
		total := roundTo(spend7d, 6)
		row.Spend7dUSD = &total
		row.Trend7d = make([]*float64, len(trend))
		for i, cost := range trend {
			value := roundTo(cost, 6)
			row.Trend7d[i] = &value
		}
	}
	ranked := append([]overviewKey{}, keys...)
	sort.SliceStable(ranked, func(i, j int) bool {
		a, b := ranked[i], ranked[j]
		if a.spendToday != b.spendToday {
			return a.spendToday > b.spendToday
		}
		if sa, sb := valueOr(a.spend7d), valueOr(b.spend7d); sa != sb {
			return sa > sb
		}
		if a.key.Name != b.key.Name {
			return a.key.Name < b.key.Name
		}
		return a.key.ID < b.key.ID
	})
	for _, key := range ranked {
		if len(row.TopKeys) == domain.GroupTopKeysLimit {
			break
		}
		top := domain.TopKey{ID: key.key.ID, Name: key.key.Name, SpendTodayUSD: key.spendToday, Spend7dUSD: key.spend7d,
			ErrorRateLastHour: roundPointer(key.usage.LastHour.Rate(), 4), LastUsedAt: key.key.LastUsedAt}
		if key.maxLimit != nil {
			top.MaxLimitPercent = roundPointer(&key.maxLimit.Percent, 2)
		}
		row.TopKeys = append(row.TopKeys, top)
	}
	return row
}

func valueOr(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

// poolSummary covers every ChatGPT account and all reported traffic.
func poolSummary(external int, accounts []overviewAccount, usage domain.OverviewUsage, now time.Time) domain.PoolSummary {
	pool := domain.PoolSummary{ExternalSources: external,
		Primary:   windowHealth(accounts, primaryWindow, 5*time.Hour, now),
		Secondary: windowHealth(accounts, secondaryWindow, 7*24*time.Hour, now)}
	for _, account := range accounts {
		pool.Accounts.Add(account.health)
	}
	days := usage.Pool.Days
	if len(days) > 0 {
		pool.SpendTodayUSD = roundTo(days[len(days)-1].CostUSD, 6)
		pool.RequestsToday = days[len(days)-1].Requests
	}
	if usage.YesterdaySameTime != nil {
		pool.SpendYesterdaySameTimeUSD = roundPointer(&usage.YesterdaySameTime.CostUSD, 6)
	}
	pool.ErrorRateLastHour = roundPointer(usage.Pool.LastHour.Rate(), 4)
	if usage.DaysKnown && len(days) == overviewDays {
		var sum float64
		for _, day := range days[len(days)-domain.OverviewTrendDays:] {
			cost := roundTo(day.CostUSD, 6)
			pool.Trend7d = append(pool.Trend7d, &cost)
			sum += day.CostUSD
		}
		pool.Spend7dUSD = roundPointer(&sum, 6)
	}
	return pool
}
