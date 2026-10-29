package application

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"codex-lb/internal/domain"
)

// KeyPageRepository is the storage the key page reads: the key, one
// snapshot of accounts, groups and keys with projected limits, the recent
// quota history for burn rates, the settings, the key's lifetime totals and
// one analytics snapshot of its usage.
type KeyPageRepository interface {
	GetAPIKey(context.Context, string) (domain.APIKey, error)
	OverviewEntities(context.Context, time.Time) (domain.OverviewEntities, error)
	RecentAccountQuotaHistory(context.Context, time.Time, time.Time) ([]domain.AccountQuota, error)
	LoadSettings(context.Context) (domain.RuntimeSettings, error)
	UsageTotals(context.Context, string, string) (domain.UsageTotals, error)
	KeyPageUsage(context.Context, domain.KeyPageUsageQuery) (domain.KeyPageUsage, error)
	KeyRecentRequests(context.Context, domain.KeyRequestsQuery) (domain.KeyRequestsPage, error)
}

// KeyPageService is use case 4 of the dashboard redesign: one key's page for
// the administrator and, redacted (use case 4.1), for the key's owner. It
// only reads.
type KeyPageService struct {
	repo  KeyPageRepository
	shell *ShellStatusService
	now   func() time.Time
}

// NewKeyPageService builds the use case; shell may be nil, which leaves the
// administrator page without a shell status.
func NewKeyPageService(repo KeyPageRepository, shell *ShellStatusService, now func() time.Time) *KeyPageService {
	if now == nil {
		now = time.Now
	}
	return &KeyPageService{repo: repo, shell: shell, now: now}
}

// KeyPageParams are the validated transport parameters of a page read:
// inclusive local dates (empty for the default 7 days ending today) in loc.
type KeyPageParams struct {
	StartDate, EndDate string
	Location           *time.Location
}

// KeyRequestsParams are the parameters of a recent-requests read; Limit 0 is
// the default page.
type KeyRequestsParams struct {
	Cursor     string
	Limit      int
	ErrorsOnly bool
}

// keyPageDefaultDays is the default period: 7 days ending today.
const keyPageDefaultDays = 7

// keyPageHistoryWindow bounds the quota history read for burn rates.
const keyPageHistoryWindow = 6 * time.Hour

// keyPagePeriod is the step "validate the period" of use case 4.
type keyPagePeriod struct {
	loc        *time.Location
	start, end time.Time // local midnights of the first day and after the last
	days       []time.Time
	previous   time.Time
	response   domain.KeyPagePeriod
}

func newKeyPagePeriod(p KeyPageParams, now time.Time) (keyPagePeriod, error) {
	loc := p.Location
	if loc == nil {
		loc = time.UTC
	}
	local := now.In(loc)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	endDay := today
	if p.EndDate != "" {
		parsed, err := time.ParseInLocation("2006-01-02", p.EndDate, loc)
		if err != nil {
			return keyPagePeriod{}, fmt.Errorf("end_date: %w", domain.ErrInvalid)
		}
		endDay = parsed
	}
	startDay := endDay.AddDate(0, 0, 1-keyPageDefaultDays)
	if p.StartDate != "" {
		parsed, err := time.ParseInLocation("2006-01-02", p.StartDate, loc)
		if err != nil {
			return keyPagePeriod{}, fmt.Errorf("start_date: %w", domain.ErrInvalid)
		}
		startDay = parsed
	}
	period := keyPagePeriod{loc: loc, start: startDay}
	for day := startDay; !day.After(endDay); day = day.AddDate(0, 0, 1) {
		if len(period.days) == MaxReportDays {
			return keyPagePeriod{}, fmt.Errorf("key page period: %w", domain.ErrInvalid)
		}
		period.days = append(period.days, day)
	}
	if len(period.days) == 0 {
		return keyPagePeriod{}, fmt.Errorf("key page period: %w", domain.ErrInvalid)
	}
	n := len(period.days)
	period.end = endDay.AddDate(0, 0, 1)
	period.previous = startDay.AddDate(0, 0, -n)
	period.response = domain.KeyPagePeriod{Start: startDay.Format("2006-01-02"), End: endDay.Format("2006-01-02"),
		Timezone: loc.String(), Days: n, PreviousStart: period.previous.Format("2006-01-02"),
		PreviousEnd: startDay.AddDate(0, 0, -1).Format("2006-01-02"), IncludesToday: !today.Before(startDay) && !today.After(endDay)}
	return period, nil
}

// AdminPage is the key page in administrator scope.
func (s *KeyPageService) AdminPage(ctx context.Context, keyID string, p KeyPageParams) (domain.KeyPageResponse, error) {
	if domain.IsInternalKey(keyID) {
		return domain.KeyPageResponse{}, domain.ErrNotFound
	}
	key, err := s.repo.GetAPIKey(ctx, keyID)
	if err != nil {
		return domain.KeyPageResponse{}, err
	}
	return s.page(ctx, domain.KeyPageAdmin, key, p)
}

// OwnerPage is the key page in owner scope for the key of an authenticated
// report session (use case 4.1).
func (s *KeyPageService) OwnerPage(ctx context.Context, key domain.APIKey, p KeyPageParams) (domain.KeyPageResponse, error) {
	return s.page(ctx, domain.KeyPageOwner, key, p)
}

// page is the workflow of use case 4: "read the key limits and period
// rollups in one snapshot --> project each limit and apply the scope".
func (s *KeyPageService) page(ctx context.Context, scope string, key domain.APIKey, p KeyPageParams) (domain.KeyPageResponse, error) {
	now := s.now().UTC()
	period, err := newKeyPagePeriod(p, now)
	if err != nil {
		return domain.KeyPageResponse{}, err
	}
	entities, err := s.repo.OverviewEntities(ctx, now)
	if err != nil {
		return domain.KeyPageResponse{}, err
	}
	entry, ok := findOverviewKey(entities.Keys, key.ID)
	switch {
	case !ok && scope == domain.KeyPageOwner, ok && scope == domain.KeyPageOwner && entry.Status(now) != "active":
		// The key changed after the session check: the snapshot decides.
		return domain.KeyPageResponse{}, ErrAuthentication
	case !ok:
		return domain.KeyPageResponse{}, domain.ErrNotFound
	}
	q := domain.KeyPageUsageQuery{KeyID: key.ID, End: period.end.UTC(), PreviousStart: period.previous.UTC(),
		PaceStart: now.Add(-domain.KeyPagePaceWindow), Now: now, PaceModels: paceModels(entry.Limits),
		Recent: domain.KeyRequestsQuery{KeyID: key.ID, Limit: domain.KeyPageRecentLimit}}
	for _, day := range period.days {
		q.DayStarts = append(q.DayStarts, day.UTC())
	}
	usage, err := s.repo.KeyPageUsage(ctx, q)
	if err != nil {
		return domain.KeyPageResponse{}, err
	}
	poolVisible := scope == domain.KeyPageAdmin
	if !poolVisible {
		settings, err := s.repo.LoadSettings(ctx)
		if err != nil {
			return domain.KeyPageResponse{}, err
		}
		poolVisible = !settings.HideUpstreamQuotaFromKeys && containsSection(key.UsageSections, "account_pool_usage")
	}
	var history []domain.AccountQuota
	if poolVisible {
		if history, err = s.repo.RecentAccountQuotaHistory(ctx, now.Add(-keyPageHistoryWindow), now); err != nil {
			return domain.KeyPageResponse{}, err
		}
	}
	response := buildKeyPage(scope, now, period, key, entry, entities, usage, history, poolVisible)
	if scope != domain.KeyPageAdmin {
		return response, nil
	}
	if s.shell != nil {
		shell, err := s.shell.Status(ctx)
		if err != nil {
			return domain.KeyPageResponse{}, err
		}
		response.Shell = &shell
	}
	lifetime, err := s.repo.UsageTotals(ctx, key.ID, "")
	if err != nil {
		return domain.KeyPageResponse{}, err
	}
	response.Lifetime = &lifetime
	return response, nil
}

// Requests is one page of the key's recent requests in a scope ("load more
// requests"). The administrator's key must exist; the owner's key is the
// authenticated one.
func (s *KeyPageService) Requests(ctx context.Context, scope, keyID string, p KeyRequestsParams) (domain.KeyRequestsResponse, error) {
	response := domain.KeyRequestsResponse{Items: []domain.KeyRequestRow{}}
	q := domain.KeyRequestsQuery{KeyID: keyID, Limit: p.Limit, ErrorsOnly: p.ErrorsOnly}
	if q.Limit == 0 {
		q.Limit = domain.KeyPageRecentLimit
	}
	if q.Limit < 1 || q.Limit > domain.KeyRequestsMaxLimit {
		return response, fmt.Errorf("limit: %w", domain.ErrInvalid)
	}
	if p.Cursor != "" {
		cursor, err := domain.ParseRequestLogCursor(p.Cursor)
		if err != nil {
			return response, err
		}
		q.Before = &cursor
	}
	if scope == domain.KeyPageAdmin {
		if domain.IsInternalKey(keyID) {
			return response, domain.ErrNotFound
		}
		if _, err := s.repo.GetAPIKey(ctx, keyID); err != nil {
			return response, err
		}
	}
	page, err := s.repo.KeyRecentRequests(ctx, q)
	if err != nil {
		return response, err
	}
	for _, item := range page.Items {
		response.Items = append(response.Items, domain.KeyRequestRowOf(item, scope))
	}
	response.NextCursor = page.NextCursor
	return response, nil
}

func findOverviewKey(keys []domain.OverviewKey, id string) (domain.OverviewKey, bool) {
	for _, key := range keys {
		if key.ID == id {
			return key, true
		}
	}
	return domain.OverviewKey{}, false
}

func containsSection(sections, name string) bool {
	for _, section := range strings.Split(sections, ",") {
		if strings.TrimSpace(section) == name {
			return true
		}
	}
	return false
}

// paceModels are the distinct model filters of the measurable limits.
func paceModels(limits []domain.LimitRule) []string {
	var models []string
	for _, limit := range limits {
		if limit.ModelFilter != nil && limit.Type != domain.LimitCredits && !contains(models, *limit.ModelFilter) {
			models = append(models, *limit.ModelFilter)
		}
	}
	sort.Strings(models)
	return models
}

// buildKeyPage is the assembly step of use case 4: properties, projected
// limits, pool or group remaining, group mini-list and the period sections,
// with the owner's fields dropped.
func buildKeyPage(scope string, now time.Time, period keyPagePeriod, key domain.APIKey, entry domain.OverviewKey,
	entities domain.OverviewEntities, usage domain.KeyPageUsage, history []domain.AccountQuota, poolVisible bool) domain.KeyPageResponse {
	admin := scope == domain.KeyPageAdmin
	labels := map[string]string{}
	for _, account := range entities.Accounts {
		labels[account.Account.ID] = accountLabel(account.Account)
	}
	response := domain.KeyPageResponse{Scope: scope, GeneratedAt: now, Period: period.response, RollupsUsable: usage.RollupsUsable,
		DetailsFrom: usage.DetailsFrom, Key: keyPageProperties(key, entry, entities, labels, admin, now),
		Limits: keyLimitStates(key.Limits, entry.Limits, usage.Pace, now), Summary: keyPeriodSummary(usage),
		Daily: keyDays(period, usage), ByModel: keyModelRows(usage), ErrorsByCode: keyErrorRows(usage),
		UserAgents: keyUserAgentRows(usage), RecentRequests: []domain.KeyRequestRow{}, NextCursor: usage.Recent.NextCursor}
	for _, item := range usage.Recent.Items {
		response.RecentRequests = append(response.RecentRequests, domain.KeyRequestRowOf(item, scope))
	}
	if entry.GroupID != nil {
		group := &domain.KeyPageGroup{ID: *entry.GroupID, Keys: []domain.KeyReportGroupKey{}}
		for _, ref := range entities.Groups {
			if ref.ID == group.ID {
				group.Name = ref.Name
			}
		}
		for _, member := range entities.Keys {
			if member.GroupID != nil && *member.GroupID == group.ID {
				group.Keys = append(group.Keys, domain.KeyReportGroupKey{ID: member.ID, Name: member.Name, IsActive: member.IsActive,
					ExpiresAt: member.ExpiresAt, IsCurrent: member.ID == key.ID, Limits: member.Limits})
			}
		}
		response.Group = group
	}
	if poolVisible {
		response.Pool = keyPagePool(entry.GroupID, entities, history, now)
	}
	if admin {
		rows := keyAccountRows(usage, labels)
		response.ByAccount = &rows
	}
	return response
}

func keyPageProperties(key domain.APIKey, entry domain.OverviewKey, entities domain.OverviewEntities, labels map[string]string,
	admin bool, now time.Time) domain.KeyPageKey {
	props := domain.KeyPageKey{ID: key.ID, Name: entry.Name, Prefix: entry.Prefix, Status: entry.Status(now), GroupID: entry.GroupID,
		CreatedAt: entry.CreatedAt, LastUsedAt: entry.LastUsedAt, ExpiresAt: entry.ExpiresAt,
		AllowedModels: nonNilStrings(key.AllowedModels), EnforcedModel: key.EnforcedModel,
		EnforcedReasoningEffort: key.EnforcedReasoningEffort, AllowedReasoningEfforts: nonNilStrings(key.AllowedReasoningEfforts),
		TrafficClass: key.TrafficClass, ServiceTier: key.EnforcedServiceTier, UsageSections: []string{},
		AccountAssignmentScopeEnabled: key.AccountAssignmentScopeEnabled, SourceAssignmentScopeEnabled: key.SourceAssignmentScopeEnabled,
		AssignedAccountCount: len(key.AssignedAccountIDs), AssignedSourceCount: len(key.AssignedSourceIDs)}
	for _, section := range strings.Split(key.UsageSections, ",") {
		if section = strings.TrimSpace(section); section != "" {
			props.UsageSections = append(props.UsageSections, section)
		}
	}
	if entry.GroupID != nil {
		for _, ref := range entities.Groups {
			if ref.ID == *entry.GroupID {
				name := ref.Name
				props.GroupName = &name
			}
		}
	}
	if admin {
		assigned := make([]domain.KeyAssignedAccount, 0, len(key.AssignedAccountIDs))
		for _, id := range key.AssignedAccountIDs {
			label, ok := labels[id]
			if !ok {
				label = id
			}
			assigned = append(assigned, domain.KeyAssignedAccount{ID: id, Label: label})
		}
		props.AssignedAccounts = &assigned
	}
	return props
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// keyLimitStates is the step "project each limit" (R21): the snapshot's
// limits, whose expired windows are already zero with the advanced reset,
// marked expired when the stored window had ended, with the pace of the
// last 3 hours and the exhaustion it reaches before the reset.
func keyLimitStates(stored, projected []domain.LimitRule, pace domain.KeyLimitPace, now time.Time) []domain.KeyLimitState {
	ended := map[int64]bool{}
	for _, limit := range stored {
		ended[limit.ID] = !limit.ResetAt.After(now)
	}
	states := make([]domain.KeyLimitState, 0, len(projected))
	for _, limit := range projected {
		state := domain.KeyLimitState{Type: limit.Type, Window: limit.Window, ModelFilter: limit.ModelFilter,
			Current: limit.CurrentValue, Max: limit.MaxValue, ResetAt: limit.ResetAt, Expired: ended[limit.ID], PaceBasis: "last_3h"}
		if limit.MaxValue > 0 {
			state.Percent = roundTo(float64(limit.CurrentValue)/float64(limit.MaxValue)*100, 2)
		}
		amounts := pace.All
		if limit.ModelFilter != nil {
			amounts = pace.ByModel[*limit.ModelFilter]
		}
		if used, ok := limitPaceAmount(limit.Type, amounts); ok {
			perHour := roundTo(float64(used)/domain.KeyPagePaceWindow.Hours(), 2)
			state.PacePerHour = &perHour
			state.ProjectedExhaustionAt = projectLimitExhaustion(limit, float64(used)/domain.KeyPagePaceWindow.Hours(), now)
		}
		states = append(states, state)
	}
	return states
}

// limitPaceAmount is the pace window's consumption in the limit's units;
// credits limits are display-only and have no pace.
func limitPaceAmount(t domain.LimitType, amounts domain.KeyPaceAmounts) (int64, bool) {
	switch t {
	case domain.LimitTotalTokens:
		return amounts.InputTokens + amounts.OutputTokens, true
	case domain.LimitInputTokens:
		return amounts.InputTokens, true
	case domain.LimitOutputTokens:
		return amounts.OutputTokens, true
	case domain.LimitCostUSD:
		return amounts.CostMicrodollars, true
	}
	return 0, false
}

// projectLimitExhaustion is the instant the remaining amount is spent at
// perHour, when that is no later than the reset; nil for a zero pace or a
// limit already at its maximum.
func projectLimitExhaustion(limit domain.LimitRule, perHour float64, now time.Time) *time.Time {
	remaining := limit.MaxValue - limit.CurrentValue
	if perHour <= 0 || remaining <= 0 {
		return nil
	}
	hours := float64(remaining) / perHour
	if hours > limit.ResetAt.Sub(now).Hours() || hours > math.MaxInt64/float64(time.Hour) {
		return nil
	}
	at := now.Add(time.Duration(hours * float64(time.Hour))).Truncate(time.Second)
	return &at
}

// keyPagePool is the pool (ungrouped key) or the key's group: window health
// over its non-deleted ChatGPT accounts as on the overview, and its
// purchased credits as in the key report.
func keyPagePool(groupID *string, entities domain.OverviewEntities, history []domain.AccountQuota, now time.Time) *domain.KeyPagePool {
	series := map[string]map[string][]domain.AccountQuota{}
	for _, row := range history {
		if series[row.AccountID] == nil {
			series[row.AccountID] = map[string][]domain.AccountQuota{}
		}
		series[row.AccountID][row.Window] = append(series[row.AccountID][row.Window], row)
	}
	pool := &domain.KeyPagePool{Scope: "pool"}
	if groupID != nil {
		pool.Scope = "group"
	}
	var accounts []overviewAccount
	credits := domain.KeyPurchasedCredits{}
	for _, entry := range entities.Accounts {
		if entry.Account.Kind != domain.AccountChatGPT || groupID != nil && !contains(entry.GroupIDs, *groupID) {
			continue
		}
		accounts = append(accounts, overviewAccountOf(entry, series[entry.Account.ID], now))
		switch {
		case entry.Credits != nil && entry.Credits.Unlimited != nil && *entry.Credits.Unlimited:
			credits.Unlimited = true
			credits.KnownAccountCount++
		case entry.Credits != nil && entry.Credits.Balance != nil:
			remaining := max(0, *entry.Credits.Balance)
			if credits.Remaining != nil {
				remaining += *credits.Remaining
			}
			credits.Remaining = &remaining
			credits.KnownAccountCount++
		}
	}
	pool.AccountCount = len(accounts)
	pool.Primary = windowHealth(accounts, primaryWindow, 5*time.Hour, now)
	pool.Secondary = windowHealth(accounts, secondaryWindow, 7*24*time.Hour, now)
	if credits.Remaining != nil {
		credits.Remaining = roundPointer(credits.Remaining, 2)
	}
	pool.PurchasedCredits = &credits
	return pool
}

func ratio(part, whole int64) *float64 {
	if whole <= 0 {
		return nil
	}
	value := roundTo(float64(part)/float64(whole), 4)
	return &value
}

func percentOf(part, whole float64) float64 {
	if whole <= 0 {
		return 0
	}
	return roundTo(part/whole*100, 2)
}

func keyPeriodSummary(usage domain.KeyPageUsage) domain.KeyPeriodSummary {
	total := usage.Total
	summary := domain.KeyPeriodSummary{Requests: total.Requests, InputTokens: total.InputTokens, OutputTokens: total.OutputTokens,
		CachedInputTokens: total.CachedInputTokens, CacheShare: ratio(total.CachedInputTokens, total.InputTokens),
		CostUSD: total.CostUSD, Errors: total.Errors, ErrorRate: ratio(total.Errors, total.Requests), Cancelled: total.Cancelled,
		TTFTP50MS: usage.TotalLatency.TTFTP50, TTFTP95MS: usage.TotalLatency.TTFTP95,
		TotalP50MS: usage.TotalLatency.TotalP50, TotalP95MS: usage.TotalLatency.TotalP95}
	if total.ReasoningKnown > 0 {
		reasoning := total.ReasoningTokens
		summary.ReasoningTokens = &reasoning
	}
	if previous := usage.Previous; previous.Requests > 0 {
		summary.Previous = &domain.KeyPeriodPrevious{Requests: previous.Requests,
			Tokens: previous.InputTokens + previous.OutputTokens, CostUSD: previous.CostUSD}
	}
	return summary
}

// keyDays names the period's top models in every day and sums the rest.
func keyDays(period keyPagePeriod, usage domain.KeyPageUsage) []domain.KeyDay {
	top := map[string]int{}
	for i, model := range usage.Models {
		if i == domain.KeyPageTopModels {
			break
		}
		top[model.Model] = i
	}
	days := make([]domain.KeyDay, 0, len(usage.Days))
	for i, day := range usage.Days {
		row := domain.KeyDay{Date: period.days[i].Format("2006-01-02"), Requests: day.Requests, InputTokens: day.InputTokens,
			OutputTokens: day.OutputTokens, CachedInputTokens: day.CachedInputTokens, CostUSD: day.CostUSD, Errors: day.Errors,
			Cancelled: day.Cancelled, ByModel: []domain.KeyDayModel{}, TTFTP50MS: day.Latency.TTFTP50, TTFTP95MS: day.Latency.TTFTP95,
			TotalP50MS: day.Latency.TotalP50, TotalP95MS: day.Latency.TotalP95, QueueP95MS: day.Latency.QueueP95, TPSP50: day.Latency.TPSP50}
		other := domain.KeyDayModel{Other: true}
		for _, model := range day.Models {
			if _, ok := top[model.Model]; ok {
				row.ByModel = append(row.ByModel, domain.KeyDayModel{Model: model.Model, Requests: model.Requests,
					Tokens: model.Tokens, CostUSD: model.CostUSD})
				continue
			}
			other.Requests += model.Requests
			other.Tokens += model.Tokens
			other.CostUSD += model.CostUSD
		}
		sort.SliceStable(row.ByModel, func(a, b int) bool { return top[row.ByModel[a].Model] < top[row.ByModel[b].Model] })
		if other.Requests > 0 {
			other.CostUSD = roundTo(other.CostUSD, 6)
			row.ByModel = append(row.ByModel, other)
		}
		days = append(days, row)
	}
	return days
}

func keyModelRows(usage domain.KeyPageUsage) []domain.KeyModelRow {
	rows := make([]domain.KeyModelRow, 0, len(usage.Models))
	for _, model := range usage.Models {
		share := percentOf(model.CostUSD, usage.Total.CostUSD)
		if usage.Total.CostUSD <= 0 {
			share = percentOf(float64(model.Requests), float64(usage.Total.Requests))
		}
		rows = append(rows, domain.KeyModelRow{Model: model.Model, Requests: model.Requests, InputTokens: model.InputTokens,
			OutputTokens: model.OutputTokens, CachedInputTokens: model.CachedInputTokens, CostUSD: model.CostUSD,
			Errors: model.Errors, Share: share, TTFTP50MS: model.TTFTP50})
	}
	return rows
}

// keyErrorRows lists the error codes and, as code "", the errors stored
// without one (including folded legacy hours).
func keyErrorRows(usage domain.KeyPageUsage) []domain.KeyErrorCodeRow {
	rows := make([]domain.KeyErrorCodeRow, 0, len(usage.ErrorCodes)+1)
	errors := usage.Total.Errors
	var coded int64
	for _, code := range usage.ErrorCodes {
		coded += code.Count
	}
	if coded > errors {
		errors = coded
	}
	for _, code := range usage.ErrorCodes {
		seen := code.LastSeenAt
		rows = append(rows, domain.KeyErrorCodeRow{Code: code.Code, Count: code.Count,
			Share: percentOf(float64(code.Count), float64(errors)), LastSeenAt: &seen})
	}
	if uncoded := errors - coded; uncoded > 0 {
		rows = append(rows, domain.KeyErrorCodeRow{Count: uncoded, Share: percentOf(float64(uncoded), float64(errors))})
	}
	sort.SliceStable(rows, func(a, b int) bool { return rows[a].Count > rows[b].Count })
	return rows
}

func keyUserAgentRows(usage domain.KeyPageUsage) []domain.KeyUserAgentRow {
	var total int64
	for _, agent := range usage.UserAgents {
		total += agent.Requests
	}
	rows := make([]domain.KeyUserAgentRow, 0, len(usage.UserAgents))
	for _, agent := range usage.UserAgents {
		rows = append(rows, domain.KeyUserAgentRow{Group: agent.Group, Requests: agent.Requests, CostUSD: agent.CostUSD,
			Share: percentOf(float64(agent.Requests), float64(total))})
	}
	return rows
}

func keyAccountRows(usage domain.KeyPageUsage, labels map[string]string) []domain.KeyAccountCostRow {
	rows := make([]domain.KeyAccountCostRow, 0, len(usage.Accounts))
	for _, account := range usage.Accounts {
		row := domain.KeyAccountCostRow{AccountID: account.AccountID, Deleted: account.Deleted, Requests: account.Requests,
			CostUSD: account.CostUSD, Share: percentOf(account.CostUSD, usage.Total.CostUSD)}
		if account.AccountID != nil && !account.Deleted {
			label, ok := labels[*account.AccountID]
			if !ok {
				label = *account.AccountID
			}
			row.Label = &label
		}
		rows = append(rows, row)
	}
	return rows
}
