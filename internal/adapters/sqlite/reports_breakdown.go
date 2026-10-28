package sqlite

import (
	"context"
	"time"

	"codex-lb/internal/domain"
)

func (s *Store) countReportConversations(ctx context.Context, filter domain.ReportFilter, start, end time.Time) (int64, error) {
	rawWhere, rawArgs := rawReportWhere(filter, start, end)
	query := `SELECT count(DISTINCT conversation_id) FROM (`
	args := make([]any, 0)
	if filter.Model == "" && filter.UserAgentGroup == "" && len(filter.APIKeyIDs) == 0 {
		query += `SELECT c.conversation_id FROM legacy_conversation_hourly c
 WHERE c.bucket_epoch>=? AND c.bucket_epoch<?`
		args = append(args, start.Unix(), end.Unix())
		if len(filter.AccountIDs) > 0 {
			ids := make([]string, len(filter.AccountIDs))
			for i, id := range filter.AccountIDs {
				ids[i] = toLegacyDimension(id)
			}
			var parts []string
			parts, args = appendInCondition(parts, args, "c.account_id", ids)
			query += " AND " + parts[0]
		}
		query += ` UNION ALL `
	}
	query += `SELECT trim(e.conversation_id) AS conversation_id FROM usage_events e WHERE ` + rawWhere + `
 AND e.conversation_id IS NOT NULL AND trim(e.conversation_id)!='')`
	args = append(args, rawArgs...)
	var count int64
	err := s.analyticsDB.QueryRowContext(ctx, query, args...).Scan(&count)
	return count, err
}
