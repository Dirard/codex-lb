package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

// keyPageRepo is a read-only fake of the key page's storage that records
// every call.
type keyPageRepo struct {
	key      domain.APIKey
	entities domain.OverviewEntities
	settings domain.RuntimeSettings
	usage    domain.KeyPageUsage
	page     domain.KeyRequestsPage
	queries  []domain.KeyPageUsageQuery
	requests []domain.KeyRequestsQuery
	calls    []string
}

func (r *keyPageRepo) GetAPIKey(_ context.Context, id string) (domain.APIKey, error) {
	r.calls = append(r.calls, "GetAPIKey")
	if id != r.key.ID {
		return domain.APIKey{}, domain.ErrNotFound
	}
	return r.key, nil
}

func (r *keyPageRepo) OverviewEntities(context.Context, time.Time) (domain.OverviewEntities, error) {
	r.calls = append(r.calls, "OverviewEntities")
	return r.entities, nil
}

func (r *keyPageRepo) RecentAccountQuotaHistory(context.Context, time.Time, time.Time) ([]domain.AccountQuota, error) {
	r.calls = append(r.calls, "RecentAccountQuotaHistory")
	return nil, nil
}

func (r *keyPageRepo) LoadSettings(context.Context) (domain.RuntimeSettings, error) {
	r.calls = append(r.calls, "LoadSettings")
	return r.settings, nil
}

func (r *keyPageRepo) UsageTotals(context.Context, string, string) (domain.UsageTotals, error) {
	r.calls = append(r.calls, "UsageTotals")
	return domain.UsageTotals{RequestCount: 9}, nil
}

func (r *keyPageRepo) KeyPageUsage(_ context.Context, q domain.KeyPageUsageQuery) (domain.KeyPageUsage, error) {
	r.calls = append(r.calls, "KeyPageUsage")
	r.queries = append(r.queries, q)
	usage := r.usage
	usage.Days = make([]domain.KeyUsageDay, len(q.DayStarts))
	copy(usage.Days, r.usage.Days)
	return usage, nil
}

func (r *keyPageRepo) KeyRecentRequests(_ context.Context, q domain.KeyRequestsQuery) (domain.KeyRequestsPage, error) {
	r.calls = append(r.calls, "KeyRecentRequests")
	r.requests = append(r.requests, q)
	return r.page, nil
}

var keyPageNow = time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)

func newKeyPageFixture() *keyPageRepo {
	group := "grp-1"
	model := "gpt-6"
	hour := time.Hour
	key := domain.APIKey{ID: "key-a", Name: "Key A", KeyPrefix: "sk-clb-a", GroupID: &group, IsActive: true,
		UsageSections: "upstream_limits,account_pool_usage", AssignedAccountIDs: []string{"acct-1"}, TrafficClass: "foreground",
		Limits: []domain.LimitRule{
			{ID: 1, Type: domain.LimitCostUSD, Window: domain.WindowDaily, MaxValue: 10_000_000, CurrentValue: 8_000_000, ResetAt: keyPageNow.Add(6 * hour)},
			{ID: 2, Type: domain.LimitTotalTokens, Window: domain.WindowFiveHours, MaxValue: 1000, CurrentValue: 700, ResetAt: keyPageNow.Add(-hour)},
			{ID: 3, Type: domain.LimitOutputTokens, Window: domain.WindowWeekly, ModelFilter: &model, MaxValue: 100, CurrentValue: 40, ResetAt: keyPageNow.Add(2 * hour)},
			{ID: 4, Type: domain.LimitCredits, Window: domain.WindowWeekly, MaxValue: 50, CurrentValue: 1, ResetAt: keyPageNow.Add(48 * hour)},
			{ID: 5, Type: domain.LimitInputTokens, Window: domain.WindowDaily, MaxValue: 10, CurrentValue: 12, ResetAt: keyPageNow.Add(hour)},
		}}
	projected := append([]domain.LimitRule(nil), key.Limits...)
	projected[1].CurrentValue, projected[1].ResetAt = 0, keyPageNow.Add(4*hour)
	account := domain.Account{ID: "acct-1", Kind: domain.AccountChatGPT, Email: "one@example.invalid", Alias: "One", PlanType: "pro", Status: domain.AccountActive}
	balance := 12.5
	accountID := "acct-1"
	code := "upstream_error"
	return &keyPageRepo{key: key,
		entities: domain.OverviewEntities{
			Accounts: []domain.OverviewAccount{{Account: account, GroupIDs: []string{group}, Credits: &domain.AccountCreditStatus{Balance: &balance}}},
			Groups:   []domain.OverviewGroupRef{{ID: group, Name: "Team"}},
			Keys: []domain.OverviewKey{
				{ID: "key-a", Name: "Key A", Prefix: "sk-clb-a", GroupID: &group, IsActive: true, CreatedAt: keyPageNow, Limits: projected},
				{ID: "key-b", Name: "Key B", Prefix: "sk-clb-b", GroupID: &group, IsActive: false, CreatedAt: keyPageNow},
				{ID: "key-c", Name: "Elsewhere", Prefix: "sk-clb-c", IsActive: true, CreatedAt: keyPageNow},
			}},
		usage: domain.KeyPageUsage{RollupsUsable: true,
			Total: domain.KeyUsageMeasures{Requests: 10, Errors: 3, InputTokens: 100, CachedInputTokens: 25, CostUSD: 2},
			Days:  []domain.KeyUsageDay{{KeyUsageMeasures: domain.KeyUsageMeasures{Requests: 10, CostUSD: 2}}},
			Models: []domain.KeyModelUsage{{Model: "gpt-6", KeyUsageMeasures: domain.KeyUsageMeasures{Requests: 6, CostUSD: 1.5}},
				{Model: "gpt-5", KeyUsageMeasures: domain.KeyUsageMeasures{Requests: 4, CostUSD: 0.5}}},
			Accounts:   []domain.KeyAccountUsage{{AccountID: &accountID, Requests: 10, CostUSD: 2}},
			ErrorCodes: []domain.KeyErrorCode{{Code: "upstream_error", Count: 2, LastSeenAt: keyPageNow.Add(-hour)}},
			// 3 hours: 3 USD, 300 tokens, 30 output tokens of gpt-6.
			Pace: domain.KeyLimitPace{All: domain.KeyPaceAmounts{InputTokens: 200, OutputTokens: 100, CostMicrodollars: 3_000_000},
				ByModel: map[string]domain.KeyPaceAmounts{"gpt-6": {OutputTokens: 30}}},
			Recent: domain.KeyRequestsPage{Items: []domain.RequestLogEntry{{RequestID: "req-1", Status: "error", ErrorCode: &code,
				AccountID: &accountID, ClientIP: &accountID, ConversationID: &accountID, UserAgent: &accountID, ErrorMessage: &accountID}}}},
		settings: domain.RuntimeSettings{}}
}

// Limits are projected at the last 3 hours' pace: a cost limit that runs out
// before its reset gets the exhaustion instant, a window that resets first
// gets none, an ended window shows zero usage, the advanced reset and its
// expired flag, a credits limit has no pace and a limit at its maximum no
// projection (R21).
func TestKeyPageProjectsLimitsAtTheRecentPace(t *testing.T) {
	repo := newKeyPageFixture()
	service := NewKeyPageService(repo, nil, func() time.Time { return keyPageNow })
	page, err := service.AdminPage(context.Background(), "key-a", KeyPageParams{})
	if err != nil {
		t.Fatal(err)
	}
	limits := page.Limits
	if len(limits) != 5 {
		t.Fatalf("%d limits", len(limits))
	}
	// 2 USD left at 1 USD per hour: 2 hours, before the reset in 6 hours.
	if at := limits[0].ProjectedExhaustionAt; at == nil || !at.Equal(keyPageNow.Add(2*time.Hour)) || *limits[0].PacePerHour != 1_000_000 || limits[0].Percent != 80 {
		t.Fatalf("cost limit %+v", limits[0])
	}
	// The ended 5 h window: zero, the advanced reset, 100 tokens per hour
	// lasts 10 hours, past the reset in 4 hours.
	if l := limits[1]; !l.Expired || l.Current != 0 || !l.ResetAt.Equal(keyPageNow.Add(4*time.Hour)) || l.ProjectedExhaustionAt != nil || *l.PacePerHour != 100 {
		t.Fatalf("expired limit %+v", l)
	}
	// gpt-6 output: 10 per hour, 60 left: 6 hours, after the reset in 2 hours.
	if l := limits[2]; l.Expired || l.ProjectedExhaustionAt != nil || *l.PacePerHour != 10 {
		t.Fatalf("model limit %+v", l)
	}
	if l := limits[3]; l.PacePerHour != nil || l.ProjectedExhaustionAt != nil {
		t.Fatalf("credits limit %+v", l)
	}
	if l := limits[4]; l.ProjectedExhaustionAt != nil || l.Percent != 120 {
		t.Fatalf("over-limit %+v", l)
	}
	if q := repo.queries[0]; len(q.PaceModels) != 1 || q.PaceModels[0] != "gpt-6" || !q.PaceStart.Equal(keyPageNow.Add(-3*time.Hour)) {
		t.Fatalf("pace query %+v", q)
	}
	for _, call := range repo.calls {
		if !strings.HasPrefix(call, "Get") && !strings.HasPrefix(call, "Overview") && !strings.HasPrefix(call, "Recent") &&
			!strings.HasPrefix(call, "Load") && !strings.HasPrefix(call, "Usage") && !strings.HasPrefix(call, "Key") {
			t.Fatalf("unexpected storage call %s", call)
		}
	}
}

// The period defaults to the 7 local days ending today, includes today only
// when it ends today or later, and is bounded to 730 days (R24).
func TestKeyPagePeriodValidation(t *testing.T) {
	prague, _ := time.LoadLocation("Europe/Prague")
	period, err := newKeyPagePeriod(KeyPageParams{Location: prague}, keyPageNow)
	if err != nil || period.response.Start != "2026-10-22" || period.response.End != "2026-10-28" || !period.response.IncludesToday ||
		period.response.PreviousStart != "2026-10-15" || period.response.PreviousEnd != "2026-10-21" || len(period.days) != 7 {
		t.Fatalf("default period %+v %v", period.response, err)
	}
	// The autumn change makes 2026-10-25 a 25-hour day in Prague.
	if got := period.days[4].AddDate(0, 0, 1).Sub(period.days[3]); got != 49*time.Hour {
		t.Fatalf("daylight-saving day %v", got)
	}
	past, err := newKeyPagePeriod(KeyPageParams{StartDate: "2026-10-01", EndDate: "2026-10-27", Location: prague}, keyPageNow)
	if err != nil || past.response.IncludesToday || past.response.Days != 27 {
		t.Fatalf("past period %+v %v", past.response, err)
	}
	for _, p := range []KeyPageParams{{StartDate: "2026-10-28", EndDate: "2026-10-27"}, {StartDate: "2024-10-27", EndDate: "2026-10-27"},
		{StartDate: "2026-13-01"}, {EndDate: "yesterday"}} {
		if _, err := newKeyPagePeriod(p, keyPageNow); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("%+v: %v", p, err)
		}
	}
	if _, err := newKeyPagePeriod(KeyPageParams{StartDate: "2024-10-29", EndDate: "2026-10-28"}, keyPageNow); err != nil {
		t.Fatalf("730 days: %v", err)
	}
}

// The owner's page drops account identities, addresses, conversations, full
// user agents, error messages, cost by account, lifetime totals, the shell
// status and assigned accounts, keeps request ids, codes and the group
// mini-list, and shows the group remaining only while upstream quotas are
// visible to the key (R26, R27).
func TestKeyPageOwnerScopeIsRedacted(t *testing.T) {
	repo := newKeyPageFixture()
	service := NewKeyPageService(repo, NewShellStatusService(nil, nil), func() time.Time { return keyPageNow })
	page, err := service.OwnerPage(context.Background(), repo.key, KeyPageParams{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"accountId"`, `"clientIp"`, `"conversationId"`, `"useragent"`, `"errorMessage"`, `"byAccount"`,
		`"lifetime"`, `"shell"`, `"assignedAccounts"`, `"planType"`, `"modelSourceId"`, "one@example.invalid"} {
		if strings.Contains(string(raw), field) {
			t.Fatalf("owner page contains %s: %s", field, raw)
		}
	}
	if page.Scope != domain.KeyPageOwner || page.RecentRequests[0].RequestID != "req-1" || *page.RecentRequests[0].ErrorCode != "upstream_error" ||
		page.Group == nil || len(page.Group.Keys) != 2 || page.Group.Name != "Team" || page.Key.AssignedAccountCount != 1 {
		t.Fatalf("owner page %s", raw)
	}
	if page.Pool == nil || page.Pool.Scope != "group" || page.Pool.AccountCount != 1 || *page.Pool.PurchasedCredits.Remaining != 12.5 {
		t.Fatalf("visible group pool %+v", page.Pool)
	}
	for _, hide := range []func(*keyPageRepo){
		func(r *keyPageRepo) { r.settings.HideUpstreamQuotaFromKeys = true },
		func(r *keyPageRepo) { r.key.UsageSections = "upstream_limits" },
		func(r *keyPageRepo) { r.key.GroupID, r.entities.Keys[0].GroupID = nil, nil },
	} {
		hidden := newKeyPageFixture()
		hide(hidden)
		page, err := NewKeyPageService(hidden, nil, func() time.Time { return keyPageNow }).OwnerPage(context.Background(), hidden.key, KeyPageParams{})
		if err != nil {
			t.Fatal(err)
		}
		visible := !hidden.settings.HideUpstreamQuotaFromKeys && containsSection(hidden.key.UsageSections, "account_pool_usage")
		if (page.Pool != nil) != visible || visible && page.Pool.Scope != "pool" {
			t.Fatalf("pool %+v with sections %q hidden %v", page.Pool, hidden.key.UsageSections, hidden.settings.HideUpstreamQuotaFromKeys)
		}
		admin, err := NewKeyPageService(hidden, nil, func() time.Time { return keyPageNow }).AdminPage(context.Background(), "key-a", KeyPageParams{})
		if err != nil || admin.Pool == nil || admin.ByAccount == nil || admin.Lifetime == nil || admin.Key.AssignedAccounts == nil {
			t.Fatalf("admin page keeps the pool and admin sections: %+v %v", admin.Pool, err)
		}
	}
	// Errors without a code are their own row; shares are of all errors.
	if rows := page.ErrorsByCode; len(rows) != 2 || rows[0].Code != "upstream_error" || rows[0].Share != 66.67 || rows[1].Code != "" || rows[1].LastSeenAt != nil {
		t.Fatalf("error rows %+v", rows)
	}
}

// A key disabled, expired or deleted between the session check and the
// snapshot is not authenticated for the owner; the administrator gets 404
// only for unknown and internal keys.
func TestKeyPageOwnerKeyIsCheckedInTheSnapshot(t *testing.T) {
	expired := keyPageNow.Add(-time.Minute)
	for name, change := range map[string]func(*keyPageRepo){
		"disabled": func(r *keyPageRepo) { r.entities.Keys[0].IsActive = false },
		"expired":  func(r *keyPageRepo) { r.entities.Keys[0].ExpiresAt = &expired },
		"deleted":  func(r *keyPageRepo) { r.entities.Keys = r.entities.Keys[1:] },
	} {
		repo := newKeyPageFixture()
		change(repo)
		service := NewKeyPageService(repo, nil, func() time.Time { return keyPageNow })
		if _, err := service.OwnerPage(context.Background(), repo.key, KeyPageParams{}); !errors.Is(err, ErrAuthentication) {
			t.Fatalf("%s: owner %v", name, err)
		}
		if _, err := service.AdminPage(context.Background(), "key-a", KeyPageParams{}); name == "deleted" && !errors.Is(err, domain.ErrNotFound) || name != "deleted" && err != nil {
			t.Fatalf("%s: admin %v", name, err)
		}
	}
	service := NewKeyPageService(newKeyPageFixture(), nil, func() time.Time { return keyPageNow })
	for _, id := range []string{"missing", domain.WarmupKeyID, domain.LocalProxyKeyID} {
		if _, err := service.AdminPage(context.Background(), id, KeyPageParams{}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("%s: %v", id, err)
		}
	}
}

// Recent requests validate the limit and cursor and project rows per scope.
func TestKeyPageRequests(t *testing.T) {
	repo := newKeyPageFixture()
	repo.page = repo.usage.Recent
	service := NewKeyPageService(repo, nil, func() time.Time { return keyPageNow })
	for _, p := range []KeyRequestsParams{{Limit: 101}, {Limit: -1}, {Cursor: "%%%"}} {
		if _, err := service.Requests(context.Background(), domain.KeyPageAdmin, "key-a", p); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("%+v: %v", p, err)
		}
	}
	cursor := domain.RequestLogCursor{RequestedAt: keyPageNow, RequestID: "req-9"}.String()
	owner, err := service.Requests(context.Background(), domain.KeyPageOwner, "key-a", KeyRequestsParams{Cursor: cursor, ErrorsOnly: true})
	if err != nil || owner.Items[0].AccountID != nil || owner.Items[0].ClientIP != nil {
		t.Fatalf("owner rows %+v %v", owner, err)
	}
	if q := repo.requests[0]; q.Limit != domain.KeyPageRecentLimit || !q.ErrorsOnly || q.Before == nil || q.Before.RequestID != "req-9" {
		t.Fatalf("requests query %+v", q)
	}
	admin, err := service.Requests(context.Background(), domain.KeyPageAdmin, "key-a", KeyRequestsParams{Limit: 100})
	if err != nil || admin.Items[0].AccountID == nil {
		t.Fatalf("admin rows %+v %v", admin, err)
	}
	if _, err := service.Requests(context.Background(), domain.KeyPageAdmin, "missing", KeyRequestsParams{}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown key %v", err)
	}
}

// The period summary carries every latency percentile of the whole period
// from the rollup histograms, queue p95 and tokens per second p50 included,
// so the period row of the latency section is complete (R22).
func TestKeyPageSummaryCarriesEveryPeriodPercentile(t *testing.T) {
	repo := newKeyPageFixture()
	value := func(v float64) *float64 { return &v }
	repo.usage.TotalLatency = domain.KeyLatency{TTFTP50: value(600), TTFTP95: value(1900), TotalP50: value(2400),
		TotalP95: value(7800), QueueP95: value(15), TPSP50: value(42.5)}
	service := NewKeyPageService(repo, nil, func() time.Time { return keyPageNow })
	page, err := service.AdminPage(context.Background(), "key-a", KeyPageParams{})
	if err != nil {
		t.Fatal(err)
	}
	s := page.Summary
	if s.TTFTP50MS == nil || *s.TTFTP50MS != 600 || s.TotalP95MS == nil || *s.TotalP95MS != 7800 ||
		s.QueueP95MS == nil || *s.QueueP95MS != 15 || s.TPSP50 == nil || *s.TPSP50 != 42.5 {
		t.Fatalf("summary percentiles %+v", s)
	}
}
