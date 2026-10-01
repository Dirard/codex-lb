package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

func TestConversationsListAndDetailsUseRetainedContentFreeEvents(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	for index, item := range []struct{ id, conversation, model, group string }{
		{"a", "conversation-one", "gpt-a", "Codex"},
		{"b", "conversation-one", "gpt-b", "Codex"},
		{"c", "conversation-two", "gpt-a", "OpenCode"},
	} {
		_, err := s.RecordUsage(ctx, domain.UsageEvent{RequestID: item.id, AccountID: "acct",
			ConversationID: item.conversation, Model: item.model, UserAgentGroup: item.group,
			RequestKind: "normal", Status: "success", RequestedAt: fixedTime.Add(time.Duration(index) * time.Minute),
			TotalLatencyMS: 100, Usage: domain.UsageAmount{InputTokens: 10, OutputTokens: 5,
				CachedInputTokens: 2, CostMicrodollars: 1000}})
		if err != nil {
			t.Fatal(err)
		}
	}
	service := application.NewReportsService(s, func() time.Time { return fixedTime.Add(time.Hour) })
	page, err := service.Conversations(ctx, application.ConversationParams{Timeframe: "1d", Limit: 1})
	if err != nil || page.Total != 2 || len(page.Conversations) != 1 || !page.HasMore ||
		page.Conversations[0].ConversationID != "conversation-two" {
		t.Fatalf("conversation pagination: %+v, %v", page, err)
	}
	page, err = service.Conversations(ctx, application.ConversationParams{Search: "one", Timeframe: "7d"})
	if err != nil || page.Total != 1 || page.Conversations[0].RequestCount != 2 ||
		page.Conversations[0].TotalTokens != 30 || page.Conversations[0].TotalCostUSD != 0.002 {
		t.Fatalf("conversation aggregate: %+v, %v", page, err)
	}
	details, err := service.ConversationDetails(ctx, "conversation-one")
	if err != nil || details.AccountCount != 1 || details.TotalElapsedTime != 200 ||
		len(details.ModelStats) != 2 || details.DominantUserAgentGroup == nil || *details.DominantUserAgentGroup != "Codex" {
		t.Fatalf("conversation details: %+v, %v", details, err)
	}
	if _, err := service.ConversationDetails(ctx, "missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown conversation accepted: %v", err)
	}
}
