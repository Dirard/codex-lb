package sqlite

import (
	"context"
	"reflect"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

func TestRequestLogPaginationFiltersBeforeLimitAndPreservesInternalOrder(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	for _, id := range []string{"acct-a", "acct-b"} {
		saveTestAccount(t, s, id)
	}
	key := testKey("key-a", nil)
	key.Name = "Budget_A%Key"
	for _, key := range []domain.APIKey{key, testKey("key-b", nil)} {
		if err := s.SaveAPIKey(ctx, key, fixedTime); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE accounts SET alias='Matching alias' WHERE id='acct-a'"); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		id, account, key, model, effort string
		at                              time.Time
	}{
		{"old", "acct-b", "key-b", "model-b", "", fixedTime.Add(-time.Hour)},
		{"id-a", "acct-a", "key-a", "model-a", "high", fixedTime},
		{"id-b", "acct-b", "key-b", "model-b", "", fixedTime},
		{"new", "acct-b", "key-b", "model-b", "", fixedTime.Add(time.Hour)},
	} {
		reservation := "reservation-" + row.id
		if _, err := s.ReserveUsage(ctx, domain.ReservationRequest{ID: reservation, APIKeyID: row.key,
			AccountID: row.account, Model: row.model, Now: row.at}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SettleUsage(ctx, reservation, domain.UsageSettlement{Status: "finalized", Event: domain.UsageEvent{RequestID: row.id, AccountID: row.account, APIKeyID: row.key,
			Model: row.model, ReasoningEffort: row.effort, RequestedAt: row.at, ConversationID: "conversation",
			Status: "success", RequestKind: "normal", Usage: domain.UsageAmount{InputTokens: 10, CostMicrodollars: 1000}}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE usage_events SET legacy_request_id=CASE request_id
 WHEN 'id-a' THEN 'display-z' ELSE 'display-a' END WHERE request_id IN ('id-a','id-b')`); err != nil {
		t.Fatal(err)
	}
	// An unrelated reservation may have the same ID as an existing usage event.
	// The page must hydrate only its selected source, not both matching IDs.
	if _, err := s.ReserveUsage(ctx, domain.ReservationRequest{ID: "id-b", APIKeyID: "key-a", AccountID: "acct-a",
		Model: "model-a", Now: fixedTime.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkReservationUncertain(ctx, "id-b"); err != nil {
		t.Fatal(err)
	}
	since, until := fixedTime, fixedTime.Add(time.Hour)
	for _, test := range []struct {
		name   string
		filter domain.RequestLogFilter
		ids    []string
		total  int64
	}{
		{"first", domain.RequestLogFilter{Limit: 2}, []string{"new", "id-b"}, 5},
		{"tied timestamp", domain.RequestLogFilter{Limit: 2, Offset: 2}, []string{"display-a", "display-z"}, 5},
		{"past end", domain.RequestLogFilter{Limit: 2, Offset: 5}, []string{}, 5},
		{"account", domain.RequestLogFilter{AccountIDs: []string{"acct-a"}}, []string{"id-b", "display-z"}, 2},
		{"key", domain.RequestLogFilter{APIKeyIDs: []string{"key-a"}, Limit: 1, Offset: 1}, []string{"display-z"}, 2},
		{"alias search", domain.RequestLogFilter{Search: "Matching alias"}, []string{"id-b", "display-z"}, 2},
		{"literal key search", domain.RequestLogFilter{Search: "_A%"}, []string{"id-b", "display-z"}, 2},
		{"time range", domain.RequestLogFilter{Since: &since, Until: &until}, []string{"id-b", "display-a", "display-z"}, 3},
		{"model", domain.RequestLogFilter{Models: []string{"model-a"}}, []string{"id-b", "display-z"}, 2},
		{"model without effort", domain.RequestLogFilter{ModelOptions: []string{"model-a"}}, []string{"id-b"}, 1},
		{"model effort", domain.RequestLogFilter{ModelOptions: []string{"model-a:::high"}}, []string{"display-z"}, 1},
		{"reasoning effort", domain.RequestLogFilter{ReasoningEfforts: []string{"high"}}, []string{"display-z"}, 1},
		{"status", domain.RequestLogFilter{Statuses: []string{"reconciliation_required"}}, []string{"id-b"}, 1},
		{"conversation", domain.RequestLogFilter{ConversationID: "conversation", Limit: 1}, []string{"new"}, 4},
	} {
		t.Run(test.name, func(t *testing.T) {
			filter := test.filter
			if filter.Limit == 0 {
				filter.Limit = 10
			}
			page, err := s.ListRequestLogs(ctx, filter)
			if err != nil {
				t.Fatal(err)
			}
			ids := make([]string, 0, len(page.Requests))
			for _, entry := range page.Requests {
				ids = append(ids, entry.RequestID)
			}
			if !reflect.DeepEqual(ids, test.ids) || page.Total != test.total || page.HasMore != (int64(filter.Offset+len(ids)) < test.total) {
				t.Fatalf("ids=%v total=%d more=%t", ids, page.Total, page.HasMore)
			}
			if filter.ConversationID != "" && (page.Conversation == nil || page.Conversation.RequestCount != 4 || page.Conversation.AggregatedCostUSD != 0.004) {
				t.Fatalf("conversation summary counted only the page: %+v", page.Conversation)
			}
		})
	}
	options, err := s.RequestLogOptions(ctx, domain.RequestLogFilter{Search: "_A%", Statuses: []string{"success"}})
	if err != nil || !reflect.DeepEqual(options.AccountIDs, []string{"acct-a"}) || len(options.APIKeys) != 1 || options.APIKeys[0].ID != "key-a" ||
		len(options.ModelOptions) != 2 || !reflect.DeepEqual(options.Statuses, []string{"reconciliation_required", "success"}) {
		t.Fatalf("filtered facets=%+v error=%v", options, err)
	}
}
