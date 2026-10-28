package sqlite

import (
	"context"
	"database/sql"
	"time"
)

// maxRetentionBatch bounds one retention job on the batch writer so accounting
// jobs never wait behind a long cleanup.
const maxRetentionBatch = 1000

func validRetentionBatch(limit int) bool { return limit >= 1 && limit <= maxRetentionBatch }

// deletableReservationSQL is the fence-preserving condition for removing a
// settled reservation r: nothing can reference its rowid again and it is not
// the largest rowid (SQLite assigns max(rowid)+1, so keeping the maximum keeps
// new reservation rowids above every deleted one).
const deletableReservationSQL = `r.status IN ('finalized','failed','released') AND r.updated_at<?
 AND r.rowid NOT IN (SELECT reservation_rowid FROM account_outcomes)
 AND r.rowid NOT IN (SELECT reservation_rowid FROM continuations)
 AND r.rowid<(SELECT max(rowid) FROM usage_reservations)`

// PruneSettledReservations deletes released reservations last updated before
// cutoff that no event, outcome or continuation references (items cascade).
// Finalized and failed reservations are removed with their request events.
func (s *Store) PruneSettledReservations(ctx context.Context, cutoff time.Time, limit int) (int, error) {
	if cutoff.IsZero() || !validRetentionBatch(limit) {
		return 0, ErrInvalid
	}
	return s.writeDeleteCount(ctx, `DELETE FROM usage_reservations WHERE rowid IN (
 SELECT r.rowid FROM usage_reservations r INDEXED BY idx_usage_reservations_released
 WHERE r.status='released' AND `+deletableReservationSQL+`
 AND NOT EXISTS (SELECT 1 FROM usage_events e WHERE e.reservation_id=r.id)
 ORDER BY r.updated_at LIMIT ?)`, millis(cutoff), limit)
}

// ThinAccountQuotaHistory keeps, for observations in [from, to), only the
// newest observation per account window, UTC hour, reset time and window
// length. The newest observation of a series is always kept because no newer
// row of the same hour exists for it.
func (s *Store) ThinAccountQuotaHistory(ctx context.Context, from, to time.Time, limit int) (int, error) {
	if from.IsZero() || !from.Before(to) || !validRetentionBatch(limit) {
		return 0, ErrInvalid
	}
	return s.writeDeleteCount(ctx, `DELETE FROM account_quota_history WHERE id IN (
 SELECT h.id FROM account_quota_history h INDEXED BY idx_account_quota_history_observed_at
 WHERE h.observed_at>=? AND h.observed_at<? AND EXISTS (
 SELECT 1 FROM account_quota_history n WHERE n.account_id=h.account_id AND n.window=h.window
 AND n.observed_at>=h.observed_at AND n.observed_at<(h.observed_at/3600000+1)*3600000
 AND (n.observed_at>h.observed_at OR n.id>h.id)
 AND n.reset_at IS h.reset_at AND n.window_minutes IS h.window_minutes)
 ORDER BY h.observed_at,h.id LIMIT ?)`, millis(from), millis(to), limit)
}

// OldestAccountQuotaObservation returns the oldest retained observation time,
// or the zero time for an empty history.
func (s *Store) OldestAccountQuotaObservation(ctx context.Context) (time.Time, error) {
	var oldest sql.NullInt64
	if err := s.readDB.QueryRowContext(ctx, `SELECT min(observed_at) FROM account_quota_history`).Scan(&oldest); err != nil {
		return time.Time{}, err
	}
	if !oldest.Valid {
		return time.Time{}, nil
	}
	return fromMillis(oldest.Int64), nil
}

// PruneAutomationRuns deletes finished runs outside the newest keepCycles
// cycles of their job and finished runs of deleted jobs. Pending and running
// runs and every job's latest cycle are kept, so a durable slot claim is never
// removed while it can still be scheduled.
func (s *Store) PruneAutomationRuns(ctx context.Context, keepCycles, limit int) (int, error) {
	if keepCycles < 1 || !validRetentionBatch(limit) {
		return 0, ErrInvalid
	}
	return s.writeDeleteCount(ctx, `DELETE FROM automation_runs WHERE rowid IN (
 WITH cycles AS (
 SELECT job_id,cycle_key,max(scheduled_for) AS at FROM automation_runs GROUP BY job_id,cycle_key
 ), ranked AS (
 SELECT job_id,cycle_key,row_number() OVER (PARTITION BY job_id ORDER BY at DESC,cycle_key DESC) AS position FROM cycles
 ) SELECT r.rowid FROM automation_runs r JOIN ranked k ON k.job_id=r.job_id AND k.cycle_key=r.cycle_key
 WHERE r.status IN ('success','failed') AND (k.position>?
 OR NOT EXISTS (SELECT 1 FROM automation_jobs j WHERE j.id=r.job_id))
 ORDER BY r.scheduled_for LIMIT ?)`, keepCycles, limit)
}

// PruneLimitWarmupAttempts deletes attempts beyond the newest keep per account
// whose reset time ended before cutoff. A past reset can never match a future
// window, so its tuple cannot be dispatched again; the newest attempt, used for
// the cooldown and attempt numbering, is always kept.
func (s *Store) PruneLimitWarmupAttempts(ctx context.Context, cutoff time.Time, keep, limit int) (int, error) {
	if cutoff.IsZero() || keep < 1 || !validRetentionBatch(limit) {
		return 0, ErrInvalid
	}
	return s.writeDeleteCount(ctx, `DELETE FROM account_limit_warmup_attempts WHERE rowid IN (
 SELECT a.rowid FROM account_limit_warmup_attempts a WHERE a.reset_at<? AND a.status<>'claimed'
 AND a.attempt<=(SELECT max(b.attempt) FROM account_limit_warmup_attempts b WHERE b.account_id=a.account_id)-?
 ORDER BY a.account_id,a.attempt LIMIT ?)`, millis(cutoff), keep, limit)
}

func (s *Store) writeDeleteCount(ctx context.Context, query string, args ...any) (int, error) {
	var count int64
	err := s.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}
		count, err = result.RowsAffected()
		return err
	})
	return int(count), err
}
