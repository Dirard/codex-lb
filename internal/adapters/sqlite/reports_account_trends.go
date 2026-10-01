package sqlite

import (
	"context"
	"database/sql"
	"fmt"
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

// RecentAccountQuotaHistory retains the EWMA tail and every observation in the
// six-hour pace horizon, so frequent polling cannot truncate the burn sample.
func (s *Store) RecentAccountQuotaHistory(ctx context.Context, since, until time.Time) ([]domain.AccountQuota, error) {
	if !since.Before(until) || until.Sub(since) > 32*24*time.Hour {
		return nil, ErrInvalid
	}
	rows, err := s.readDB.QueryContext(ctx, `WITH ranked AS (
 SELECT account_id,window,observed_at,used_percent,reset_at,window_minutes,id,
 row_number() OVER (PARTITION BY account_id,window ORDER BY observed_at DESC,id DESC) AS position
 FROM account_quota_history WHERE observed_at>=? AND observed_at<=?
 ) SELECT account_id,window,observed_at,used_percent,reset_at,window_minutes
 FROM ranked WHERE position<=64 OR observed_at>=?
 ORDER BY account_id,window,observed_at,id LIMIT 100001`, millis(since), millis(until), millis(until.Add(-6*time.Hour)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.AccountQuota{}
	for rows.Next() {
		// ponytail: fail explicitly above 100k samples; page per account if real installations outgrow this bound.
		if len(result) == 100000 {
			return nil, fmt.Errorf("quota projection history exceeds 100000 observations")
		}
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
	return result, rows.Err()
}
