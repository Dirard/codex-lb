package application

import (
	"context"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

var overviewTestNow = time.Date(2026, 10, 28, 12, 30, 0, 0, time.UTC)

type overviewFake struct {
	entities domain.OverviewEntities
	usage    domain.OverviewUsage
	history  []domain.AccountQuota
	query    domain.OverviewUsageQuery
}

func (f *overviewFake) OverviewEntities(context.Context, time.Time) (domain.OverviewEntities, error) {
	return f.entities, nil
}

func (f *overviewFake) OverviewUsage(_ context.Context, q domain.OverviewUsageQuery) (domain.OverviewUsage, error) {
	f.query = q
	return f.usage, nil
}

func (f *overviewFake) RecentAccountQuotaHistory(context.Context, time.Time, time.Time) ([]domain.AccountQuota, error) {
	return f.history, nil
}

func (f *overviewFake) ShellSnapshot(context.Context) (domain.ShellSnapshot, error) {
	last := overviewTestNow.Add(-time.Minute)
	return domain.ShellSnapshot{LastSyncAt: &last, RollupsUsable: true}, nil
}

func (f *overviewFake) LoadSettings(context.Context) (domain.RuntimeSettings, error) {
	return domain.RuntimeSettings{RoutingStrategy: "usage_weighted"}, nil
}

func (f *overviewFake) Health(context.Context) error { return nil }

func days(costs ...float64) []domain.UsageDay {
	out := make([]domain.UsageDay, len(costs))
	for i, cost := range costs {
		out[i] = domain.UsageDay{Requests: int64(cost), CostUSD: cost}
	}
	return out
}

func chatgptAccount(id string, status domain.AccountStatus, used float64, groups ...string) domain.OverviewAccount {
	reset := overviewTestNow.Add(2 * time.Hour)
	weekly := overviewTestNow.Add(48 * time.Hour)
	return domain.OverviewAccount{Account: domain.Account{ID: id, Kind: domain.AccountChatGPT, Email: id + "@example.invalid",
		PlanType: "plus", Status: status}, GroupIDs: groups,
		Quotas: []domain.AccountQuota{{AccountID: id, Window: "primary", UsedPercent: used, ResetAt: &reset, ObservedAt: overviewTestNow},
			{AccountID: id, Window: "secondary", UsedPercent: used / 2, ResetAt: &weekly, ObservedAt: overviewTestNow}}}
}

func runOverview(t *testing.T, fake *overviewFake) domain.OverviewResponse {
	t.Helper()
	service := NewOverviewService(fake, NewShellStatusService(fake, nil), func() time.Time { return overviewTestNow })
	response, err := service.Overview(context.Background(), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func rowByName(t *testing.T, response domain.OverviewResponse, name string) domain.GroupRow {
	t.Helper()
	for _, row := range response.Groups {
		if row.Name == name {
			return row
		}
	}
	t.Fatalf("no row %q", name)
	return domain.GroupRow{}
}

// A key moved from group A to group B counts in B for every day, because
// spend is attributed by current membership (R6, usage-rollups D4).
func TestOverviewAttributesSpendByCurrentMembership(t *testing.T) {
	b := "grp-b"
	fake := &overviewFake{
		entities: domain.OverviewEntities{
			Groups: []domain.OverviewGroupRef{{ID: "grp-a", Name: "A"}, {ID: "grp-b", Name: "B"}},
			Keys: []domain.OverviewKey{{ID: "moved", Name: "moved", GroupID: &b, IsActive: true},
				{ID: "free", Name: "free", IsActive: true}, {ID: "off", Name: "off", GroupID: &b}},
		},
		usage: domain.OverviewUsage{RollupsUsable: true, DaysKnown: true,
			Pool: domain.OverviewScopeUsage{Days: days(9, 9, 9, 9, 9, 9, 9, 9)},
			Keys: map[string]domain.OverviewScopeUsage{
				"moved": {Days: days(1, 2, 3, 4, 5, 6, 7, 8)},
				"free":  {Days: days(0, 0, 0, 0, 0, 0, 0, 1)},
				"off":   {Days: days(0, 0, 0, 0, 0, 0, 0, 0.5)},
			}},
	}
	response := runOverview(t, fake)
	a, grouped, ungrouped := rowByName(t, response, "A"), rowByName(t, response, "B"), rowByName(t, response, "Ungrouped keys")
	if a.SpendTodayUSD != 0 || *a.Spend7dUSD != 0 {
		t.Fatalf("group A keeps nothing of the moved key: %+v", a)
	}
	if grouped.SpendTodayUSD != 8.5 || *grouped.Spend7dUSD != 2+3+4+5+6+7+8+0.5 || *grouped.Trend7d[0] != 2 || *grouped.Trend7d[6] != 8.5 {
		t.Fatalf("group B: today %v, 7d %v, trend %v", grouped.SpendTodayUSD, *grouped.Spend7dUSD, grouped.Trend7d)
	}
	if grouped.Keys != (domain.KeyCounts{Active: 1, Total: 2}) || ungrouped.ID != nil || ungrouped.SpendTodayUSD != 1 {
		t.Fatalf("keys %+v ungrouped %+v", grouped.Keys, ungrouped)
	}
	if response.Pool.SpendTodayUSD != 9 || *response.Pool.Spend7dUSD != 63 || len(response.Pool.Trend7d) != 7 {
		t.Fatalf("pool %+v", response.Pool)
	}
	if len(fake.query.DayStarts) != 8 || !fake.query.DayStarts[7].Equal(time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)) ||
		!fake.query.LastHourStart.Equal(time.Date(2026, 10, 28, 11, 0, 0, 0, time.UTC)) ||
		!fake.query.YesterdaySameTime.Equal(overviewTestNow.Add(-24*time.Hour)) {
		t.Fatalf("query %+v", fake.query)
	}
}

// Rows are ordered by attention score, then problem accounts, then keys at
// or above 90 %, then spend today, then name; the ungrouped row follows the
// same rule (R12).
func TestOverviewOrdersGroupRows(t *testing.T) {
	ids := []string{"g-critical", "g-warning", "g-problem", "g-near", "g-spend", "g-a", "g-b"}
	groupID := func(i int) *string { return &ids[i] }
	limit := func(current int64) []domain.LimitRule {
		return []domain.LimitRule{{Type: domain.LimitTotalTokens, Window: domain.WindowWeekly, MaxValue: 100, CurrentValue: current,
			ResetAt: overviewTestNow.Add(24 * time.Hour)}}
	}
	expires := overviewTestNow.Add(72 * time.Hour)
	fake := &overviewFake{
		entities: domain.OverviewEntities{
			Accounts: []domain.OverviewAccount{chatgptAccount("ok", domain.AccountActive, 10, "g-a", "g-b", "g-spend", "g-near", "g-warning", "g-critical"),
				chatgptAccount("low", domain.AccountActive, 92, "g-problem")},
			Keys: []domain.OverviewKey{
				{ID: "blocked", Name: "blocked", GroupID: groupID(0), IsActive: true, Limits: limit(100)},
				{ID: "expiring", Name: "expiring", GroupID: groupID(1), IsActive: true, ExpiresAt: &expires},
				{ID: "near", Name: "near", GroupID: groupID(3), IsActive: true, Limits: limit(91)},
				{ID: "rich", Name: "rich", GroupID: groupID(4), IsActive: true},
				{ID: "ka", Name: "ka", GroupID: groupID(5), IsActive: true},
				{ID: "kb", Name: "kb", GroupID: groupID(6), IsActive: true},
			},
		},
		usage: domain.OverviewUsage{RollupsUsable: true, DaysKnown: true, Pool: domain.OverviewScopeUsage{Days: days(0, 0, 0, 0, 0, 0, 0, 0)},
			Keys: map[string]domain.OverviewScopeUsage{"rich": {Days: days(0, 0, 0, 0, 0, 0, 0, 5)}}},
	}
	for i, id := range ids {
		fake.entities.Groups = append(fake.entities.Groups, domain.OverviewGroupRef{ID: id, Name: []string{"Z critical", "Y warning", "X problem", "W near", "V spend", "A", "B"}[i]})
	}
	response := runOverview(t, fake)
	var names []string
	for _, row := range response.Groups {
		names = append(names, row.Name)
	}
	// Both warning rows have score 1; the near-limit key orders W first. The
	// ungrouped row sees the whole pool, so the low account is its problem
	// too, and its name orders it before X.
	want := []string{"Z critical", "W near", "Y warning", "Ungrouped keys", "X problem", "V spend", "A", "B"}
	if len(names) != len(want) {
		t.Fatalf("rows %v", names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("order %v, want %v", names, want)
		}
	}
	critical := rowByName(t, response, "Z critical")
	if critical.Attention != "critical" || critical.LimitFill.KeysBlocked != 1 || *critical.LimitFill.MaxPercent != 100 {
		t.Fatalf("critical row %+v", critical)
	}
	if rowByName(t, response, "X problem").ProblemAccounts[0].Health != domain.HealthLow {
		t.Fatal("problem accounts list the low account")
	}
	if response.AttentionKeysTotal != 3 || response.AttentionKeys[0].ID != "blocked" || response.AttentionKeys[0].PrimaryAction != "open_group_limits" {
		t.Fatalf("attention %+v total %d", response.AttentionKeys, response.AttentionKeysTotal)
	}
}

// Account health, capacity-weighted windows and the runway come from the
// accounts' quotas and history; re-login accounts raise critical alerts that
// make their groups and the ungrouped row critical.
func TestOverviewAccountHealthAndAlerts(t *testing.T) {
	fake := &overviewFake{
		entities: domain.OverviewEntities{
			Accounts: []domain.OverviewAccount{chatgptAccount("burning", domain.AccountActive, 60, "g"),
				chatgptAccount("relogin", domain.AccountReauthRequired, 0, "g"),
				{Account: domain.Account{ID: "ext", Kind: domain.AccountExternal, Status: domain.AccountActive}}},
			Groups: []domain.OverviewGroupRef{{ID: "g", Name: "G"}},
		},
		usage: domain.OverviewUsage{RollupsUsable: true, DaysKnown: true, Pool: domain.OverviewScopeUsage{Days: days(0, 0, 0, 0, 0, 0, 0, 0)}},
	}
	// 20 % per hour on a 225-credit window: 45 credits/h; 90 credits left
	// run out in 2 h, before the reset in 2 h? exactly at it: no.
	for i := range 4 {
		fake.history = append(fake.history, domain.AccountQuota{AccountID: "burning", Window: "primary",
			UsedPercent: 40 + float64(i)*20.0/3, ResetAt: fake.entities.Accounts[0].Quotas[0].ResetAt,
			ObservedAt: overviewTestNow.Add(time.Duration(i-3) * 20 * time.Minute)})
	}
	response := runOverview(t, fake)
	if response.Pool.Accounts != (domain.HealthCounts{OK: 1, Reauth: 1}) || response.Pool.ExternalSources != 1 {
		t.Fatalf("pool health %+v external %d", response.Pool.Accounts, response.Pool.ExternalSources)
	}
	primary := response.Pool.Primary
	if primary.CoveredAccounts != 2 || primary.TotalAccounts != 2 || *primary.RemainingPercent != 70 || *primary.BurnCreditsPerHour != 45 {
		t.Fatalf("primary %+v", primary)
	}
	group := rowByName(t, response, "G")
	if group.Attention != "critical" || len(group.ProblemAccounts) != 1 || group.ProblemAccounts[0].Action != "reauth" {
		t.Fatalf("group %+v", group)
	}
	if rowByName(t, response, "Ungrouped keys").Attention != "critical" {
		t.Fatal("the ungrouped row covers the whole pool")
	}
	if len(response.Alerts) == 0 || response.Alerts[0].Rule != domain.AlertAccountReauth || response.Alerts[0].Action.Target != "relogin" {
		t.Fatalf("alerts %+v", response.Alerts)
	}
	if response.Shell.LastSyncAt == nil || response.Shell.RoutingStrategy != "usage_weighted" || !response.Shell.Ready {
		t.Fatalf("shell %+v", response.Shell)
	}
}

func TestOverviewUsageQueryInPrague(t *testing.T) {
	prague, err := time.LoadLocation("Europe/Prague")
	if err != nil {
		t.Fatal(err)
	}
	// 2026-10-26 01:30 local (CET) is after the 10-25 change: the 25-hour
	// day starts at 2026-10-24T22:00Z.
	q := OverviewUsageQuery(time.Date(2026, 10, 26, 0, 30, 0, 0, time.UTC), prague)
	if !q.DayStarts[7].Equal(time.Date(2026, 10, 25, 23, 0, 0, 0, time.UTC)) || !q.DayStarts[6].Equal(time.Date(2026, 10, 24, 22, 0, 0, 0, time.UTC)) {
		t.Fatalf("day starts %v", q.DayStarts)
	}
	// 01:30 local yesterday was still summer time (UTC+2).
	if !q.YesterdaySameTime.Equal(time.Date(2026, 10, 24, 23, 30, 0, 0, time.UTC)) {
		t.Fatalf("yesterday %v", q.YesterdaySameTime)
	}
	if !q.TrailingStart.Equal(time.Date(2026, 10, 24, 23, 0, 0, 0, time.UTC)) {
		t.Fatalf("trailing %v", q.TrailingStart)
	}
}
