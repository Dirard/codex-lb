package sqlite

import (
	"fmt"
	"strings"
	"time"

	"codex-lb/internal/domain"
)

// reportFacts combines folded hourly measures with the unfurled event tail.
// Imported raw rows below their historical fold watermark are excluded once.
func reportFacts(filter domain.ReportFilter, start, end time.Time) (string, []any) {
	hWhere, hArgs := hourlyReportWhere(filter, start, end)
	rWhere, rArgs := rawReportWhere(filter, start, end)
	query := `WITH facts AS (
 SELECT h.bucket_epoch*1000 AS at_ms,h.account_id,h.api_key_id,h.model,
 h.request_count,h.input_tokens,h.output_or_reasoning_tokens AS output_tokens,
 h.reasoning_tokens,h.reasoning_known_count,
 h.cached_input_tokens_clamped AS cached_input_tokens,h.cost_usd,
 h.error_count,h.cancelled_count FROM legacy_hourly_usage h WHERE ` + hWhere + `
 UNION ALL
 SELECT e.requested_at,
 CASE WHEN e.account_id IS NULL THEN char(31) WHEN substr(e.account_id,1,1)=char(31)
 THEN char(31)||e.account_id ELSE e.account_id END,
 CASE WHEN e.api_key_id IS NULL THEN char(31) WHEN substr(e.api_key_id,1,1)=char(31)
 THEN char(31)||e.api_key_id ELSE e.api_key_id END,
 e.model,1,e.input_tokens,e.output_tokens,e.reasoning_tokens,e.reasoning_tokens_known,
 max(0,min(e.cached_input_tokens,e.input_tokens)),e.cost_microdollars/1000000.0,
 CASE WHEN e.status NOT IN ('success','completed','cancelled') THEN 1 ELSE 0 END,
 CASE WHEN e.status='cancelled' THEN 1 ELSE 0 END
 FROM usage_events e WHERE ` + rWhere + `)
`
	return query, append(hArgs, rArgs...)
}

func hourlyReportWhere(filter domain.ReportFilter, start, end time.Time) (string, []any) {
	parts := []string{"h.bucket_epoch>=?", "h.bucket_epoch<?", "h.request_kind NOT IN ('warmup','limit_warmup')"}
	args := []any{start.Unix(), end.Unix()}
	if filter.UserAgentGroup != "" {
		parts = append(parts, "0=1")
	}
	if len(filter.AccountIDs) > 0 {
		values := make([]string, len(filter.AccountIDs))
		for i, id := range filter.AccountIDs {
			values[i] = toLegacyDimension(id)
		}
		parts, args = appendInCondition(parts, args, "h.account_id", values)
	}
	if len(filter.APIKeyIDs) > 0 {
		values := make([]string, len(filter.APIKeyIDs))
		for i, id := range filter.APIKeyIDs {
			values[i] = toLegacyDimension(id)
		}
		parts, args = appendInCondition(parts, args, "h.api_key_id", values)
	}
	if filter.Model != "" {
		parts = append(parts, "h.model=?")
		args = append(args, filter.Model)
	}
	return strings.Join(parts, " AND "), args
}

func rawReportWhere(filter domain.ReportFilter, start, end time.Time) (string, []any) {
	parts := []string{"e.requested_at>=?", "e.requested_at<?",
		"e.request_kind NOT IN ('warmup','limit_warmup')",
		`(e.legacy_request_id IS NULL OR e.requested_at>=coalesce(
 (SELECT hourly_folded_through FROM legacy_import_state WHERE id=1),-9223372036854775808))`}
	args := []any{millis(start), millis(end)}
	if len(filter.AccountIDs) > 0 {
		parts, args = appendInCondition(parts, args, "e.account_id", filter.AccountIDs)
	}
	if len(filter.APIKeyIDs) > 0 {
		parts, args = appendInCondition(parts, args, "e.api_key_id", filter.APIKeyIDs)
	}
	if filter.Model != "" {
		parts = append(parts, "e.model=?")
		args = append(args, filter.Model)
	}
	if filter.UserAgentGroup != "" {
		if filter.UserAgentGroup == "Missing User-Agent" {
			parts = append(parts, "e.useragent_group IS NULL")
		} else {
			parts = append(parts, "e.useragent_group=?")
			args = append(args, filter.UserAgentGroup)
		}
	}
	return strings.Join(parts, " AND "), args
}

func appendInCondition(parts []string, args []any, column string, values []string) ([]string, []any) {
	if len(values) == 0 {
		return parts, args
	}
	parts = append(parts, column+" IN ("+strings.TrimRight(strings.Repeat("?,", len(values)), ",")+")")
	for _, v := range values {
		args = append(args, v)
	}
	return parts, args
}

func checkedReportWindow(start, end time.Time) error {
	if start.IsZero() || !start.Before(end) || end.Sub(start) > 731*24*time.Hour {
		return fmt.Errorf("report window: %w", ErrInvalid)
	}
	return nil
}
