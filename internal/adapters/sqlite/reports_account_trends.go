package sqlite

import (
	"context"
	"database/sql"
	"slices"
	"time"

	"codex-lb/internal/domain"
)

func (s *Store) AccountQuotaTrendBuckets(ctx context.Context, id string, since, until time.Time) ([]domain.AccountQuotaTrendBucket, error) {
	if id == "" || !since.Before(until) || until.Sub(since) > 8*24*time.Hour {
		return nil, ErrInvalid
	}
	rows, err := s.readDB.QueryContext(ctx, `WITH samples AS (
 SELECT window,(observed_at/3600000)*3600000 AS hour,observed_at,used_percent,
 reset_at,window_minutes,id FROM account_quota_history
 WHERE account_id=? AND observed_at>=? AND observed_at<?
 ), grouped AS (
 SELECT window,hour,avg(used_percent) AS used_percent FROM samples GROUP BY window,hour
 ), ranked AS (
 SELECT *,row_number() OVER (PARTITION BY window,hour ORDER BY observed_at DESC,id DESC) AS position
 FROM samples
 ) SELECT grouped.window,grouped.hour,ranked.observed_at,grouped.used_percent,
 ranked.reset_at,ranked.window_minutes FROM grouped JOIN ranked
 ON ranked.window=grouped.window AND ranked.hour=grouped.hour AND ranked.position=1
 ORDER BY grouped.hour,grouped.window`, id, millis(since), millis(until))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]domain.AccountQuotaTrendBucket, 0)
	for rows.Next() {
		var bucket domain.AccountQuotaTrendBucket
		var at, observed int64
		var reset, minutes sql.NullInt64
		if err := rows.Scan(&bucket.Window, &at, &observed, &bucket.UsedPercent, &reset, &minutes); err != nil {
			return nil, err
		}
		bucket.At, bucket.ObservedAt = fromMillis(at), fromMillis(observed)
		bucket.ResetAt = optionalTime(reset)
		if minutes.Valid {
			value := int(minutes.Int64)
			bucket.WindowMinutes = &value
		}
		result = append(result, bucket)
	}
	return result, rows.Err()
}

// PruneAccountQuotaHistory keeps the latest observation per account/window,
// even when that observation is older than the cutoff.
func (s *Store) PruneAccountQuotaHistory(ctx context.Context, cutoff time.Time, limit int) (int, error) {
	if cutoff.IsZero() || limit < 1 || limit > 1000 {
		return 0, ErrInvalid
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM account_quota_history WHERE id IN (
 SELECT h.id FROM account_quota_history h WHERE h.observed_at<? AND EXISTS (
 SELECT 1 FROM account_quota_history newer WHERE newer.account_id=h.account_id
 AND newer.window=h.window AND (newer.observed_at>h.observed_at OR
 (newer.observed_at=h.observed_at AND newer.id>h.id)))
 ORDER BY h.observed_at,h.id LIMIT ?)`, millis(cutoff), limit)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// recentQuotaHistoryQuery walks each current series once through its index,
// newest first, down to the older of the pace-horizon start and the 64th newest
// observation of the lookback. No step scans or sorts the whole history.
const recentQuotaHistoryQuery = `SELECT h.account_id,h.window,h.observed_at,h.used_percent,h.reset_at,h.window_minutes
 FROM account_quotas s CROSS JOIN account_quota_history h INDEXED BY idx_account_quota_history_identity_time
 ON h.account_id=s.account_id AND h.window=s.window
 WHERE h.observed_at<=? AND h.observed_at>=min(?,coalesce((SELECT t.observed_at FROM account_quota_history t
 WHERE t.account_id=s.account_id AND t.window=s.window AND t.observed_at>=? AND t.observed_at<=?
 ORDER BY t.observed_at DESC,t.id DESC LIMIT 1 OFFSET ?),?))
 ORDER BY s.account_id,s.window,h.observed_at DESC,h.id DESC`

// quotaPaceHorizon and quotaPaceTail bound the projection history per series:
// the six-hour burn horizon plus the newest observations for the EWMA tail.
const (
	quotaPaceHorizon = 6 * time.Hour
	quotaPaceTail    = 64
)

// RecentAccountQuotaHistory reads the projection input per current account
// window: every observation in the six-hour pace horizon plus the 64 newest
// observations of the lookback, oldest first within each series. The result
// grows with the number of series and the history resolution, never fails on
// installation size, and reads only the rows it returns.
func (s *Store) RecentAccountQuotaHistory(ctx context.Context, since, until time.Time) ([]domain.AccountQuota, error) {
	if !since.Before(until) || until.Sub(since) > 32*24*time.Hour {
		return nil, ErrInvalid
	}
	horizon := max(millis(since), millis(until.Add(-quotaPaceHorizon)))
	rows, err := s.readDB.QueryContext(ctx, recentQuotaHistoryQuery,
		millis(until), horizon, millis(since), millis(until), quotaPaceTail-1, millis(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.AccountQuota{}
	for rows.Next() {
		var quota domain.AccountQuota
		var observed int64
		var reset, minutes sql.NullInt64
		if err := rows.Scan(&quota.AccountID, &quota.Window, &observed, &quota.UsedPercent, &reset, &minutes); err != nil {
			return nil, err
		}
		quota.ObservedAt = fromMillis(observed)
		quota.ResetAt = optionalTime(reset)
		if minutes.Valid {
			value := int(minutes.Int64)
			quota.WindowMinutes = &value
		}
		result = append(result, quota)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Rows arrive newest first per series; consumers expect chronological order.
	for start := 0; start < len(result); {
		end := start
		for end < len(result) && result[end].AccountID == result[start].AccountID && result[end].Window == result[start].Window {
			end++
		}
		slices.Reverse(result[start:end])
		start = end
	}
	return result, nil
}
