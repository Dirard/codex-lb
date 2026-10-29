package application

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

// groupsFake answers the groups summary from the overview fake's snapshot
// plus the limit templates and the span before the overview's days.
type groupsFake struct {
	*overviewFake
	limits    map[string][]domain.LimitTemplate
	spanKnown bool
	spanPool  domain.UsageDay
	spanKeys  map[string]domain.UsageDay
	query     domain.GroupsSummaryUsageQuery
}

func (f *groupsFake) GroupsSummaryEntities(ctx context.Context, now time.Time) (domain.GroupsSummaryEntities, error) {
	entities, err := f.OverviewEntities(ctx, now)
	return domain.GroupsSummaryEntities{OverviewEntities: entities, Limits: f.limits}, err
}

func (f *groupsFake) GroupsSummaryUsage(ctx context.Context, q domain.GroupsSummaryUsageQuery) (domain.GroupsSummaryUsage, error) {
	f.query = q
	usage, err := f.OverviewUsage(ctx, q.Overview)
	return domain.GroupsSummaryUsage{Overview: usage, SpanKnown: f.spanKnown, SpanPool: f.spanPool, SpanKeys: f.spanKeys}, err
}

func runGroupsSummary(t *testing.T, fake *groupsFake, loc *time.Location) domain.GroupsSummary {
	t.Helper()
	service := NewGroupsSummaryService(fake, NewShellStatusService(fake, nil), func() time.Time { return overviewTestNow })
	summary, err := service.Summary(context.Background(), loc)
	if err != nil {
		t.Fatal(err)
	}
	return summary
}

// groupsFixture has two groups sharing an account, an external source, a
// re-login account, a burning account, grouped, ungrouped and disabled keys
// with limits, and spend on every day of the span and the overview's days.
func groupsFixture() *groupsFake {
	a, b := "grp-a", "grp-b"
	limit := func(current int64) []domain.LimitRule {
		return []domain.LimitRule{{Type: domain.LimitCostUSD, Window: domain.WindowWeekly, MaxValue: 100, CurrentValue: current,
			ResetAt: overviewTestNow.Add(24 * time.Hour)}}
	}
	weekly := "gpt-test"
	overview := &overviewFake{
		entities: domain.OverviewEntities{
			Accounts: []domain.OverviewAccount{chatgptAccount("burning", domain.AccountActive, 60, a, b),
				chatgptAccount("relogin", domain.AccountReauthRequired, 0, a),
				chatgptAccount("idle", domain.AccountActive, 5),
				{Account: domain.Account{ID: "ext", Kind: domain.AccountExternal, Email: "ext@example.invalid", Status: domain.AccountActive},
					GroupIDs: []string{b}}},
			Groups: []domain.OverviewGroupRef{{ID: a, Name: "Alpha"}, {ID: b, Name: "Beta"}},
			Keys: []domain.OverviewKey{
				{ID: "k-a1", Name: "a1", Prefix: "sk-a1", GroupID: &a, IsActive: true, Limits: limit(95)},
				{ID: "k-a2", Name: "a2", Prefix: "sk-a2", GroupID: &a, IsActive: true},
				{ID: "k-b1", Name: "b1", Prefix: "sk-b1", GroupID: &b},
				{ID: "k-free", Name: "free", Prefix: "sk-free", IsActive: true, Limits: limit(10)},
			},
		},
		usage: domain.OverviewUsage{RollupsUsable: true, DaysKnown: true,
			Pool: domain.OverviewScopeUsage{Days: days(10, 10, 10, 10, 10, 10, 10, 12)},
			Keys: map[string]domain.OverviewScopeUsage{
				"k-a1":   {Days: days(1, 1, 1, 1, 1, 1, 1, 2)},
				"k-a2":   {Days: days(0, 0, 0, 0, 0, 0, 3, 0)},
				"k-b1":   {Days: days(0.5, 0, 0, 0, 0, 0, 0, 0)},
				"k-free": {Days: days(2, 2, 2, 2, 2, 2, 2, 4)},
			}},
	}
	for i := range 4 {
		overview.history = append(overview.history, domain.AccountQuota{AccountID: "burning", Window: "primary",
			UsedPercent: 40 + float64(i)*20.0/3, ResetAt: overview.entities.Accounts[0].Quotas[0].ResetAt,
			ObservedAt: overviewTestNow.Add(time.Duration(i-3) * 20 * time.Minute)})
	}
	return &groupsFake{overviewFake: overview, spanKnown: true,
		limits: map[string][]domain.LimitTemplate{a: {{Type: domain.LimitCostUSD, Window: domain.WindowWeekly, MaxValue: 50_000_000},
			{Type: domain.LimitTotalTokens, Window: domain.WindowDaily, ModelFilter: &weekly, MaxValue: 5_000_000}}},
		spanPool: domain.UsageDay{Requests: 300, CostUSD: 220},
		spanKeys: map[string]domain.UsageDay{"k-a1": {Requests: 20, CostUSD: 20}, "k-b1": {Requests: 4, CostUSD: 4},
			"k-free": {Requests: 40, CostUSD: 44}, "k-gone": {Requests: 9, CostUSD: 9}}}
}

// The summary's group rows and the ungrouped row are the overview's rows for
// the same snapshot, in the overview's order (R40).
func TestGroupsSummaryRowsEqualTheOverviewRows(t *testing.T) {
	for _, zone := range []string{"UTC", "Europe/Prague", "Asia/Kolkata"} {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Fatal(err)
		}
		fake := groupsFixture()
		summary := runGroupsSummary(t, fake, loc)
		service := NewOverviewService(fake.overviewFake, NewShellStatusService(fake.overviewFake, nil), func() time.Time { return overviewTestNow })
		overview, err := service.Overview(context.Background(), loc)
		if err != nil {
			t.Fatal(err)
		}
		if len(summary.Groups) != len(overview.Groups) {
			t.Fatalf("%s: %d summary rows, %d overview rows", zone, len(summary.Groups), len(overview.Groups))
		}
		for i, row := range summary.Groups {
			if !reflect.DeepEqual(row.GroupRow, overview.Groups[i]) {
				t.Fatalf("%s row %d:\nsummary  %+v\noverview %+v", zone, i, row.GroupRow, overview.Groups[i])
			}
		}
		if !reflect.DeepEqual(fake.query.Overview, OverviewUsageQuery(overviewTestNow, loc)) {
			t.Fatalf("%s: the summary reads the overview's usage query, got %+v", zone, fake.query.Overview)
		}
		if summary.Timezone != zone || summary.Shell.RoutingStrategy != "usage_weighted" {
			t.Fatalf("%s: header %+v", zone, summary)
		}
	}
}

func summaryRow(t *testing.T, summary domain.GroupsSummary, name string) domain.GroupSummaryRow {
	t.Helper()
	for _, row := range summary.Groups {
		if row.Name == name {
			return row
		}
	}
	t.Fatalf("no row %q", name)
	return domain.GroupSummaryRow{}
}

// Thirty-day spend is the span before the overview's days plus those 8 days,
// summed over current member keys; deleted keys of the span count nowhere but
// in the pool (R40).
func TestGroupsSummaryThirtyDaySpend(t *testing.T) {
	summary := runGroupsSummary(t, groupsFixture(), time.UTC)
	alpha, beta, ungrouped := summaryRow(t, summary, "Alpha"), summaryRow(t, summary, "Beta"), summaryRow(t, summary, "Ungrouped keys")
	for _, c := range []struct {
		row  domain.GroupSummaryRow
		want float64
	}{{alpha, 20 + 9 + 3}, {beta, 4 + 0.5}, {ungrouped, 44 + 18}} {
		if c.row.Spend30dUSD == nil || *c.row.Spend30dUSD != c.want {
			t.Fatalf("%s 30-day spend %v, want %v", c.row.Name, c.row.Spend30dUSD, c.want)
		}
	}
	if *alpha.Spend7dUSD != 1*6+2+3 || alpha.SpendTodayUSD != 2 {
		t.Fatalf("alpha 7 d %v today %v", *alpha.Spend7dUSD, alpha.SpendTodayUSD)
	}
	pool := summary.Pool
	if pool.ID != nil || pool.Spend30dUSD == nil || *pool.Spend30dUSD != 220+82 || pool.SpendTodayUSD != 12 || *pool.Spend7dUSD != 72 ||
		pool.Keys != (domain.KeyCounts{Active: 3, Total: 4}) || pool.Accounts != (domain.HealthCounts{OK: 2, Reauth: 1}) ||
		pool.Attention != "critical" {
		t.Fatalf("pool row %+v", pool.GroupRow)
	}
	if pool.LimitFill.KeysAtOrAbove90 != 1 || len(pool.TopKeys7d) != 0 || len(pool.AccountIDs) != 0 || len(pool.Limits) != 0 {
		t.Fatalf("pool fill %+v keys %v", pool.LimitFill, pool.TopKeys7d)
	}

	fake := groupsFixture()
	fake.spanKnown = false
	unknown := runGroupsSummary(t, fake, time.UTC)
	if unknown.Pool.Spend30dUSD != nil || summaryRow(t, unknown, "Alpha").Spend30dUSD != nil || summaryRow(t, unknown, "Alpha").TopKeys7d[0].Spend30dUSD != nil {
		t.Fatal("an unknown span leaves every 30-day spend null")
	}
}

// Rows carry their limit templates, member accounts and member keys by 7-day
// spend; accounts list health and remaining for the member picker (R40).
func TestGroupsSummaryTemplatesMembersAndKeys(t *testing.T) {
	summary := runGroupsSummary(t, groupsFixture(), time.UTC)
	alpha, beta, ungrouped := summaryRow(t, summary, "Alpha"), summaryRow(t, summary, "Beta"), summaryRow(t, summary, "Ungrouped keys")
	if len(alpha.Limits) != 2 || alpha.Limits[1].ModelFilter == nil || *alpha.Limits[1].ModelFilter != "gpt-test" || len(beta.Limits) != 0 || beta.Limits == nil {
		t.Fatalf("templates alpha %+v beta %+v", alpha.Limits, beta.Limits)
	}
	if !reflect.DeepEqual(alpha.AccountIDs, []string{"burning", "relogin"}) || !reflect.DeepEqual(beta.AccountIDs, []string{"burning", "ext"}) ||
		ungrouped.AccountIDs == nil || len(ungrouped.AccountIDs) != 0 || len(ungrouped.Limits) != 0 {
		t.Fatalf("members alpha %v beta %v ungrouped %v", alpha.AccountIDs, beta.AccountIDs, ungrouped.AccountIDs)
	}
	if len(alpha.TopKeys7d) != 2 || alpha.TopKeys7d[0].ID != "k-a1" || *alpha.TopKeys7d[0].Spend30dUSD != 29 ||
		*alpha.TopKeys7d[0].MaxLimitPercent != 95 || alpha.TopKeys7d[0].Prefix != "sk-a1" || alpha.TopKeys7d[1].Status != "active" {
		t.Fatalf("alpha keys %+v", alpha.TopKeys7d)
	}
	if beta.TopKeys7d[0].Status != "disabled" || *beta.TopKeys7d[0].Spend7dUSD != 0 {
		t.Fatalf("beta keys %+v", beta.TopKeys7d)
	}
	if alpha.Attention != "critical" {
		t.Fatalf("alpha holds the re-login account: %s", alpha.Attention)
	}
	if len(summary.Accounts) != 4 {
		t.Fatalf("accounts %+v", summary.Accounts)
	}
	for _, account := range summary.Accounts {
		switch account.ID {
		case "relogin":
			if account.Health != domain.HealthReauth || account.Kind != domain.AccountChatGPT {
				t.Fatalf("relogin %+v", account)
			}
		case "idle":
			if account.Health != domain.HealthOK || *account.PrimaryRemainingPercent != 95 || *account.SecondaryRemainingPercent != 97.5 {
				t.Fatalf("idle %+v", account)
			}
		case "ext":
			if account.Kind != domain.AccountExternal || account.Health != domain.HealthOK || account.PrimaryRemainingPercent != nil ||
				account.Label != "ext@example.invalid" {
				t.Fatalf("external %+v", account)
			}
		}
	}
}

// At most 20 member keys are listed, by 7-day spend, then spend today and
// name; the pool's attention comes from whole-pool alerts only, so a blocked
// member key marks its group and not the pool.
func TestGroupsSummaryTopKeysAndPoolAttention(t *testing.T) {
	g := "grp"
	fake := &groupsFake{overviewFake: &overviewFake{
		entities: domain.OverviewEntities{Groups: []domain.OverviewGroupRef{{ID: g, Name: "G"}},
			Accounts: []domain.OverviewAccount{chatgptAccount("ok", domain.AccountActive, 10, g)}},
		usage: domain.OverviewUsage{RollupsUsable: true, DaysKnown: true, Pool: domain.OverviewScopeUsage{Days: days(0, 0, 0, 0, 0, 0, 0, 0)},
			Keys: map[string]domain.OverviewScopeUsage{}},
	}, spanKnown: true}
	for i := range 25 {
		id := fmt.Sprintf("k%02d", i)
		key := domain.OverviewKey{ID: id, Name: id, GroupID: &g, IsActive: true}
		if i == 0 {
			key.Limits = []domain.LimitRule{{Type: domain.LimitCostUSD, Window: domain.WindowWeekly, MaxValue: 100, CurrentValue: 100,
				ResetAt: overviewTestNow.Add(24 * time.Hour)}}
		}
		fake.entities.Keys = append(fake.entities.Keys, key)
		fake.usage.Keys[id] = domain.OverviewScopeUsage{Days: days(0, float64(i%5), 0, 0, 0, 0, 0, float64(i%3))}
	}
	summary := runGroupsSummary(t, fake, time.UTC)
	keys := summaryRow(t, summary, "G").TopKeys7d
	if len(keys) != domain.GroupKeysLimit {
		t.Fatalf("%d keys listed", len(keys))
	}
	for i := 1; i < len(keys); i++ {
		if domain.GroupKeyLess(keys[i], keys[i-1]) {
			t.Fatalf("keys out of order at %d: %+v", i, keys)
		}
	}
	if keys[0].ID != "k14" || *keys[0].Spend7dUSD != 6 {
		t.Fatalf("first key %+v", keys[0])
	}
	if summary.Pool.Attention != "none" || summaryRow(t, summary, "G").Attention != "critical" {
		t.Fatalf("pool attention %s", summary.Pool.Attention)
	}
}

func TestGroupsSummaryUsageQuerySpansThirtyLocalDays(t *testing.T) {
	prague, err := time.LoadLocation("Europe/Prague")
	if err != nil {
		t.Fatal(err)
	}
	// 2026-10-28 is in CET (UTC+1); 29 days earlier, 2026-09-29, was in CEST.
	q := GroupsSummaryUsageQuery(overviewTestNow, prague)
	if !q.SpanStart.Equal(time.Date(2026, 9, 28, 22, 0, 0, 0, time.UTC)) || !q.Overview.DayStarts[0].Equal(time.Date(2026, 10, 20, 22, 0, 0, 0, time.UTC)) {
		t.Fatalf("span %v first day %v", q.SpanStart, q.Overview.DayStarts[0])
	}
}
