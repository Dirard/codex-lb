package application

import (
	"context"
	"strings"
	"time"

	"codex-lb/internal/domain"
)

type ConversationParams struct {
	Limit, Offset     int
	Search, Timeframe string
	Since             *time.Time
}

func (s *ReportsService) Conversations(ctx context.Context, p ConversationParams) (domain.ConversationsResponse, error) {
	if p.Limit == 0 {
		p.Limit = 50
	}
	if p.Limit < 1 || p.Limit > 1000 || p.Offset < 0 || len(p.Search) > 256 || p.Since != nil && p.Timeframe != "" {
		return domain.ConversationsResponse{}, domain.ErrInvalid
	}
	now := s.now().UTC()
	since := now.Add(-30 * 24 * time.Hour)
	if p.Since != nil && p.Since.After(since) {
		since = p.Since.UTC()
	}
	if p.Timeframe != "" {
		switch p.Timeframe {
		case "1d":
			since = now.Add(-24 * time.Hour)
		case "7d":
			since = now.Add(-7 * 24 * time.Hour)
		case "30d":
		default:
			return domain.ConversationsResponse{}, domain.ErrInvalid
		}
	}
	return s.repo.ListConversations(ctx, domain.ConversationFilter{
		Since: since, Search: strings.TrimSpace(p.Search), Limit: p.Limit, Offset: p.Offset})
}

func (s *ReportsService) ConversationDetails(ctx context.Context, id string) (domain.ConversationDetails, error) {
	if strings.TrimSpace(id) == "" || len(id) > 512 {
		return domain.ConversationDetails{}, domain.ErrInvalid
	}
	return s.repo.ConversationDetails(ctx, id)
}
