package sqlite

import (
	"context"
	"slices"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

func TestRequestLogsProjectUncertainReservationsWithoutFabricatedUsage(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	for _, id := range []string{"acct-a", "acct-b"} {
		saveTestAccount(t, s, id)
	}
	for _, id := range []string{"key-a", "key-b"} {
		if err := s.SaveAPIKey(ctx, testKey(id, nil), fixedTime); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []struct {
		id, key, account, model string
		flagged                 bool
	}{
		{"pending-a", "key-a", "acct-a", "model-pending", true},
		{"pending-b", "key-b", "acct-b", "model-other", true},
		{"live", "key-a", "acct-a", "model-live", false},
		{"already-recorded", "key-a", "acct-a", "model-recorded", true},
	} {
		if _, err := s.ReserveUsage(ctx, domain.ReservationRequest{ID: item.id, APIKeyID: item.key, AccountID: item.account, Model: item.model, Now: fixedTime.Add(time.Minute)}); err != nil {
			t.Fatal(err)
		}
		if item.flagged {
			if _, err := s.MarkReservationUncertain(ctx, item.id); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := s.SettleUsage(ctx, "already-recorded", domain.UsageSettlement{Status: "finalized", Event: domain.UsageEvent{RequestID: "known-request", AccountID: "acct-a", APIKeyID: "key-a", Model: "model-recorded", RequestKind: "chat", Status: "success", RequestedAt: fixedTime, Usage: domain.UsageAmount{InputTokens: 2, OutputTokens: 1, CostMicrodollars: 500}}}); err != nil {
		t.Fatal(err)
	}
	page := func(filter domain.RequestLogFilter) domain.RequestLogsResponse {
		t.Helper()
		if filter.Limit == 0 {
			filter.Limit = 10
		}
		result, err := s.ListRequestLogs(ctx, filter)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	first := page(domain.RequestLogFilter{Limit: 1})
	if first.Total != 3 || !first.HasMore || len(first.Requests) != 1 || first.Requests[0].RequestID != "pending-b" {
		t.Fatalf("combined first page: %+v", first)
	}
	second := page(domain.RequestLogFilter{Limit: 1, Offset: 1})
	entry := second.Requests[0]
	if entry.RequestID != "pending-a" || entry.Status != "reconciliation_required" || entry.RequestKind != "unknown" ||
		entry.AccountID == nil || *entry.AccountID != "acct-a" || entry.APIKeyID == nil || *entry.APIKeyID != "key-a" ||
		entry.ErrorCode == nil || entry.ErrorMessage == nil || entry.InputTokens != nil || entry.OutputTokens != nil || entry.Tokens != nil || entry.CachedInputTokens != nil || entry.ReasoningTokens != nil || entry.CostUSD != nil ||
		entry.LatencyMS != nil || entry.LatencyQueueMS != nil || entry.ServiceTier != nil || entry.ReasoningEffort != nil || entry.Transport != nil || entry.PlanType != nil {
		t.Fatalf("pending row invented usage or metadata: %+v", entry)
	}
	last := page(domain.RequestLogFilter{Limit: 1, Offset: 2})
	if last.HasMore || last.Requests[0].RequestID != "known-request" || last.Requests[0].InputTokens == nil || *last.Requests[0].InputTokens != 2 {
		t.Fatalf("known row changed or pending duplicate exists: %+v", last)
	}
	since, until := fixedTime.Add(30*time.Second), fixedTime.Add(2*time.Minute)
	for _, filter := range []domain.RequestLogFilter{
		{AccountIDs: []string{"acct-a"}, Statuses: []string{"reconciliation_required"}},
		{APIKeyIDs: []string{"key-a"}, Models: []string{"model-pending"}},
		{ModelOptions: []string{"model-pending"}},
		{Search: "pending-a", Since: &since, Until: &until},
	} {
		result := page(filter)
		if result.Total != 1 || result.Requests[0].RequestID != "pending-a" {
			t.Fatalf("pending filter mismatch: %+v %+v", filter, result)
		}
	}
	if got := page(domain.RequestLogFilter{Search: "live"}); got.Total != 0 {
		t.Fatal("ordinary active reservation exposed")
	}
	if got := page(domain.RequestLogFilter{ConversationID: "not-recorded"}); got.Total != 0 {
		t.Fatal("unknown conversation was guessed")
	}
	options, err := s.RequestLogOptions(ctx, domain.RequestLogFilter{Statuses: []string{"success"}})
	if err != nil || !slices.Contains(options.Statuses, "reconciliation_required") || !slices.Contains(options.Statuses, "success") || len(options.APIKeys) != 2 || len(options.ModelOptions) != 3 {
		t.Fatalf("combined facets missing pending rows: %+v %v", options, err)
	}
	totals, err := s.UsageTotals(ctx, "key-a", "")
	if err != nil || totals.RequestCount != 1 || totals.Usage.CostMicrodollars != 500 {
		t.Fatalf("read projection affected totals: %+v %v", totals, err)
	}
	settlement := domain.UsageSettlement{Status: "finalized", Event: domain.UsageEvent{RequestID: "confirmed-pending-a", APIKeyID: "key-a", AccountID: "acct-a", Model: "model-pending", RequestKind: "reconciled", Status: "success", RequestedAt: fixedTime.Add(time.Minute), Usage: domain.UsageAmount{InputTokens: 4, OutputTokens: 2, CostMicrodollars: 600}}}
	if _, err := s.SettleUsage(ctx, "pending-a", settlement); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SettleUsage(ctx, "pending-b", domain.UsageSettlement{Status: "released"}); err != nil {
		t.Fatal(err)
	}
	final := page(domain.RequestLogFilter{})
	if final.Total != 2 || page(domain.RequestLogFilter{Statuses: []string{"reconciliation_required"}}).Total != 0 || final.Requests[0].RequestID != "confirmed-pending-a" {
		t.Fatalf("reconciliation duplicated or retained pending rows: %+v", final)
	}
	totals, err = s.UsageTotals(ctx, "key-a", "")
	if err != nil || totals.RequestCount != 2 || totals.Usage.CostMicrodollars != 1100 {
		t.Fatalf("reconciliation changed totals twice: %+v %v", totals, err)
	}
}
