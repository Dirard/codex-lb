package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"codex-lb/internal/domain"
)

const videoAccountColumns = `id,xai_user_id,email,label,status,access_token_encrypted,refresh_token_encrypted,
 oidc_client_id,access_expires_at,token_version,cooldown_until,last_refusal_code,last_used_at,created_at,updated_at,
 usage_percent,usage_period_end,usage_checked_at,usage_error`

const videoJobColumns = `id,api_key_id,account_id,coalesce(upstream_request_id,''),model,mode,requested_seconds,status,
 video_url,duration_seconds,cost_microdollars,error_code,next_poll_at,created_at,finished_at`

func positiveMillisTime(value int64) *time.Time {
	if value <= 0 {
		return nil
	}
	at := fromMillis(value)
	return &at
}

func nullableMillisTime(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}
	at := fromMillis(value.Int64)
	return &at
}

func scanVideoAccount(row interface{ Scan(...any) error }) (domain.VideoAccount, error) {
	var a domain.VideoAccount
	var expires, cooldown, used, created, updated, usageEnd, usageChecked int64
	var usage sql.NullFloat64
	err := row.Scan(&a.ID, &a.XAIUserID, &a.Email, &a.Label, &a.Status, &a.AccessTokenEncrypted, &a.RefreshTokenEncrypted,
		&a.OIDCClientID, &expires, &a.TokenVersion, &cooldown, &a.LastRefusalCode, &used, &created, &updated,
		&usage, &usageEnd, &usageChecked, &a.UsageError)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	if err != nil {
		return a, err
	}
	a.AccessExpiresAt, a.CreatedAt, a.UpdatedAt = fromMillis(expires), fromMillis(created), fromMillis(updated)
	a.CooldownUntil, a.LastUsedAt = positiveMillisTime(cooldown), positiveMillisTime(used)
	a.UsagePeriodEnd, a.UsageCheckedAt = positiveMillisTime(usageEnd), positiveMillisTime(usageChecked)
	if usage.Valid {
		a.UsagePercent = &usage.Float64
	}
	return a, nil
}

func scanVideoJob(row interface{ Scan(...any) error }) (domain.VideoJob, error) {
	var j domain.VideoJob
	var duration sql.NullFloat64
	var cost, next, finished sql.NullInt64
	var created int64
	err := row.Scan(&j.ID, &j.APIKeyID, &j.AccountID, &j.UpstreamRequestID, &j.Model, &j.Mode, &j.RequestedSeconds, &j.Status,
		&j.VideoURL, &duration, &cost, &j.ErrorCode, &next, &created, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return j, ErrNotFound
	}
	if err != nil {
		return j, err
	}
	j.CreatedAt, j.NextPollAt, j.FinishedAt = fromMillis(created), nullableMillisTime(next), nullableMillisTime(finished)
	if duration.Valid {
		j.DurationSeconds = &duration.Float64
	}
	if cost.Valid {
		j.CostMicrodollars = &cost.Int64
	}
	return j, nil
}

// SaveVideoAccount adds an account or, for the same xAI user, replaces its
// tokens and returns it to service. The stored row is returned.
func (s *Store) SaveVideoAccount(ctx context.Context, account domain.VideoAccount) (domain.VideoAccount, error) {
	var saved domain.VideoAccount
	err := s.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO video_accounts(id,xai_user_id,email,label,status,access_token_encrypted,
 refresh_token_encrypted,oidc_client_id,access_expires_at,created_at,updated_at) VALUES(?,?,?,?,'active',?,?,?,?,?,?)
 ON CONFLICT(xai_user_id) DO UPDATE SET email=excluded.email,status='active',access_token_encrypted=excluded.access_token_encrypted,
 refresh_token_encrypted=excluded.refresh_token_encrypted,oidc_client_id=excluded.oidc_client_id,
 access_expires_at=excluded.access_expires_at,token_version=video_accounts.token_version+1,refresh_claimed_until=0,
 cooldown_until=0,last_refusal_code='',usage_error='',updated_at=excluded.updated_at`,
			account.ID, account.XAIUserID, account.Email, account.Label, account.AccessTokenEncrypted, account.RefreshTokenEncrypted,
			account.OIDCClientID, millis(account.AccessExpiresAt), millis(account.CreatedAt), millis(account.UpdatedAt))
		if err != nil {
			return err
		}
		saved, err = scanVideoAccount(tx.QueryRowContext(ctx, "SELECT "+videoAccountColumns+" FROM video_accounts WHERE xai_user_id=?", account.XAIUserID))
		return err
	})
	return saved, err
}

func (s *Store) GetVideoAccount(ctx context.Context, id string) (domain.VideoAccount, error) {
	return scanVideoAccount(s.readDB.QueryRowContext(ctx, "SELECT "+videoAccountColumns+" FROM video_accounts WHERE id=?", id))
}

func (s *Store) ListVideoAccounts(ctx context.Context) ([]domain.VideoAccount, map[string]domain.VideoAccountStats, error) {
	rows, err := s.readDB.QueryContext(ctx, "SELECT "+videoAccountColumns+" FROM video_accounts ORDER BY created_at,id")
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	accounts := []domain.VideoAccount{}
	for rows.Next() {
		account, err := scanVideoAccount(rows)
		if err != nil {
			return nil, nil, err
		}
		accounts = append(accounts, account)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	stats := map[string]domain.VideoAccountStats{}
	statRows, err := s.readDB.QueryContext(ctx, `SELECT account_id,
 sum(status IN ('dispatching','pending')),sum(status='done'),coalesce(sum(cost_microdollars),0)
 FROM video_jobs GROUP BY account_id`)
	if err != nil {
		return nil, nil, err
	}
	defer statRows.Close()
	for statRows.Next() {
		var id string
		var entry domain.VideoAccountStats
		if err := statRows.Scan(&id, &entry.Unfinished, &entry.Done, &entry.CostMicrodollars); err != nil {
			return nil, nil, err
		}
		stats[id] = entry
	}
	return accounts, stats, statRows.Err()
}

// SetVideoAccountLabel renames an account.
func (s *Store) SetVideoAccountLabel(ctx context.Context, id, label string, now time.Time) error {
	return s.videoAccountUpdate(ctx, `UPDATE video_accounts SET label=?,updated_at=? WHERE id=?`, label, millis(now), id)
}

// SetVideoAccountStatus changes the status; returning to active clears the
// cooldown and refusal so the operator's decision takes effect at once.
func (s *Store) SetVideoAccountStatus(ctx context.Context, id string, status domain.VideoAccountStatus, code string, now time.Time) error {
	if status == domain.VideoAccountActive {
		return s.videoAccountUpdate(ctx, `UPDATE video_accounts SET status='active',cooldown_until=0,last_refusal_code='',updated_at=? WHERE id=?`, millis(now), id)
	}
	return s.videoAccountUpdate(ctx, `UPDATE video_accounts SET status=?,last_refusal_code=?,updated_at=? WHERE id=?`, status, code, millis(now), id)
}

// SetVideoCooldown takes an account out of selection until the given moment.
func (s *Store) SetVideoCooldown(ctx context.Context, id, code string, until, now time.Time) error {
	return s.videoAccountUpdate(ctx, `UPDATE video_accounts SET cooldown_until=?,last_refusal_code=?,updated_at=? WHERE id=?`, millis(until), code, millis(now), id)
}

func (s *Store) videoAccountUpdate(ctx context.Context, query string, args ...any) error {
	return s.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// DeleteVideoAccount removes an account and ends its unfinished jobs: nothing
// can read them any more. Finished jobs stay for the totals.
func (s *Store) DeleteVideoAccount(ctx context.Context, id string, now time.Time) error {
	return s.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE video_jobs SET status='unknown',error_code='video_account_deleted',finished_at=?,next_poll_at=NULL
 WHERE account_id=? AND status IN ('dispatching','pending')`, millis(now), id); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM video_accounts WHERE id=?`, id)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// ClaimVideoRefresh lets one worker refresh an account at a time. The claim
// expires by itself, so a crashed worker does not block the account.
func (s *Store) ClaimVideoRefresh(ctx context.Context, id string, version int64, until, now time.Time) (bool, error) {
	claimed := false
	err := s.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE video_accounts SET refresh_claimed_until=? WHERE id=? AND token_version=? AND refresh_claimed_until<=?`,
			millis(until), id, version, millis(now))
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		claimed = count == 1
		return err
	})
	return claimed, err
}

// SaveVideoTokens stores the result of a refresh unless newer tokens exist.
func (s *Store) SaveVideoTokens(ctx context.Context, id string, version int64, access, refresh []byte, expires, now time.Time) (bool, error) {
	saved := false
	err := s.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE video_accounts SET access_token_encrypted=?,refresh_token_encrypted=?,access_expires_at=?,
 token_version=token_version+1,refresh_claimed_until=0,updated_at=? WHERE id=? AND token_version=?`,
			access, refresh, millis(expires), millis(now), id, version)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		saved = count == 1
		return err
	})
	return saved, err
}

// SaveVideoUsage records a successful read of the account's pool.
func (s *Store) SaveVideoUsage(ctx context.Context, id string, percent float64, periodEnd, now time.Time) error {
	return s.videoAccountUpdate(ctx, `UPDATE video_accounts SET usage_percent=?,usage_period_end=?,usage_checked_at=?,usage_error='' WHERE id=?`,
		percent, millis(periodEnd), millis(now), id)
}

// SetVideoUsageError records why the pool could not be read and keeps the last
// value that was read.
func (s *Store) SetVideoUsageError(ctx context.Context, id, code string, now time.Time) error {
	return s.videoAccountUpdate(ctx, `UPDATE video_accounts SET usage_error=?,usage_checked_at=? WHERE id=?`, code, millis(now), id)
}

// IsVideoKeyAllowed reports whether the key is on the video allow list.
func (s *Store) IsVideoKeyAllowed(ctx context.Context, keyID string) (bool, error) {
	var found int
	err := s.readDB.QueryRowContext(ctx, `SELECT 1 FROM video_allowed_keys WHERE api_key_id=?`, keyID).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// ListVideoKeys returns usable keys with their allow-list state, listed first.
func (s *Store) ListVideoKeys(ctx context.Context) ([]domain.VideoKeyAccess, error) {
	rows, err := s.readDB.QueryContext(ctx, `SELECT k.id,k.name,k.key_prefix,k.is_active,v.api_key_id IS NOT NULL
 FROM api_keys k LEFT JOIN video_allowed_keys v ON v.api_key_id=k.id
 WHERE k.deleted_at IS NULL AND k.id NOT IN (?,?) ORDER BY v.api_key_id IS NULL,k.name,k.id`, domain.LocalProxyKeyID, domain.WarmupKeyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := []domain.VideoKeyAccess{}
	for rows.Next() {
		var key domain.VideoKeyAccess
		if err := rows.Scan(&key.ID, &key.Name, &key.KeyPrefix, &key.IsActive, &key.Allowed); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

// AllowVideoKey adds an existing key; an unknown or internal key is refused.
func (s *Store) AllowVideoKey(ctx context.Context, keyID string, now time.Time) error {
	if domain.IsInternalKey(keyID) {
		return ErrNotFound
	}
	return s.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var found int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM api_keys WHERE id=? AND deleted_at IS NULL`, keyID).Scan(&found)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO video_allowed_keys(api_key_id,created_at) VALUES(?,?) ON CONFLICT(api_key_id) DO NOTHING`, keyID, millis(now))
		return err
	})
}

func (s *Store) DenyVideoKey(ctx context.Context, keyID string) error {
	return s.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM video_allowed_keys WHERE api_key_id=?`, keyID)
		return err
	})
}

// ReserveVideoJob picks an account in one short transaction and writes the
// dispatching job with it, so concurrent requests and overlapping workers count
// the job at once. An account whose pool is known to be exhausted is skipped.
// Accounts are ordered by pool usage in 10-point bands, so similar accounts
// share the work, then by unfinished jobs and least recent use. An account with
// no reading comes after those with one; an elapsed period counts as unused.
// It returns ErrNoAccounts when none qualifies.
func (s *Store) ReserveVideoJob(ctx context.Context, job domain.VideoJob, excluded map[string]bool, maxUnfinished int, now time.Time) (domain.VideoAccount, error) {
	var selected domain.VideoAccount
	err := s.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT a.id,
 (SELECT count(*) FROM video_jobs j WHERE j.account_id=a.id AND j.status IN ('dispatching','pending')) AS unfinished
 FROM video_accounts a WHERE a.status='active' AND a.cooldown_until<=?
 AND NOT (a.usage_percent>=100 AND a.usage_period_end>?)
 ORDER BY CASE WHEN a.usage_percent IS NULL THEN 999 WHEN a.usage_period_end<=? THEN 0 ELSE CAST(a.usage_percent/10 AS INTEGER) END,
 unfinished,a.last_used_at,a.id`, millis(now), millis(now), millis(now))
		if err != nil {
			return err
		}
		chosen := ""
		for rows.Next() {
			var id string
			var unfinished int
			if err := rows.Scan(&id, &unfinished); err != nil {
				_ = rows.Close()
				return err
			}
			if chosen == "" && !excluded[id] && unfinished < maxUnfinished {
				chosen = id
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if chosen == "" {
			return ErrNoAccounts
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO video_jobs(id,api_key_id,account_id,model,mode,requested_seconds,status,created_at)
 VALUES(?,?,?,?,?,?,'dispatching',?)`, job.ID, job.APIKeyID, chosen, job.Model, job.Mode, job.RequestedSeconds, millis(job.CreatedAt)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE video_accounts SET last_used_at=? WHERE id=?`, millis(now), chosen); err != nil {
			return err
		}
		selected, err = scanVideoAccount(tx.QueryRowContext(ctx, "SELECT "+videoAccountColumns+" FROM video_accounts WHERE id=?", chosen))
		return err
	})
	return selected, err
}

// NearestVideoCooldown returns when the first active account that is cooling
// down or has an exhausted pool can be used again.
func (s *Store) NearestVideoCooldown(ctx context.Context, now time.Time) (*time.Time, error) {
	var until sql.NullInt64
	err := s.readDB.QueryRowContext(ctx, `SELECT min(max(cooldown_until,CASE WHEN usage_percent>=100 THEN usage_period_end ELSE 0 END))
 FROM video_accounts WHERE status='active' AND (cooldown_until>? OR (usage_percent>=100 AND usage_period_end>?))`, millis(now), millis(now)).Scan(&until)
	if err != nil {
		return nil, err
	}
	return nullableMillisTime(until), nil
}

// MarkVideoJobPending records the upstream id of a created job.
func (s *Store) MarkVideoJobPending(ctx context.Context, id, upstreamRequestID string, nextPoll time.Time) error {
	return s.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE video_jobs SET status='pending',upstream_request_id=?,next_poll_at=? WHERE id=? AND status='dispatching'`,
			upstreamRequestID, millis(nextPoll), id)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count == 0 {
			return ErrConflict
		}
		return nil
	})
}

// FinishVideoJob applies the one exit from an unfinished job. It reports false
// when another poll or worker finished the job first, so a cost counts once.
func (s *Store) FinishVideoJob(ctx context.Context, id string, finish domain.VideoJobFinish) (bool, error) {
	applied := false
	err := s.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE video_jobs SET status=?,video_url=?,duration_seconds=?,cost_microdollars=?,error_code=?,finished_at=?,next_poll_at=NULL
 WHERE id=? AND status IN ('dispatching','pending')`, finish.Status, finish.VideoURL, finish.DurationSeconds, finish.CostMicrodollars, finish.ErrorCode, millis(finish.At), id)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		applied = count == 1
		return err
	})
	return applied, err
}

func (s *Store) ScheduleVideoPoll(ctx context.Context, id string, next time.Time) error {
	return s.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE video_jobs SET next_poll_at=? WHERE id=? AND status='pending'`, millis(next), id)
		return err
	})
}

func (s *Store) GetVideoJob(ctx context.Context, id string) (domain.VideoJob, error) {
	return scanVideoJob(s.readDB.QueryRowContext(ctx, "SELECT "+videoJobColumns+" FROM video_jobs WHERE id=?", id))
}

// ListVideoJobs pages one key's jobs newest first. Refused attempts are an
// internal detail of the request that moved to another account.
func (s *Store) ListVideoJobs(ctx context.Context, keyID string, before *domain.VideoJobCursor, limit int) ([]domain.VideoJob, error) {
	query := "SELECT " + videoJobColumns + " FROM video_jobs WHERE api_key_id=? AND status<>'refused'"
	args := []any{keyID}
	if before != nil {
		query += " AND (created_at,id)<(?,?)"
		args = append(args, millis(before.CreatedAt), before.ID)
	}
	query += " ORDER BY created_at DESC,id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.readDB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := []domain.VideoJob{}
	for rows.Next() {
		job, err := scanVideoJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

// DueVideoJobs returns pending jobs whose next poll has come.
func (s *Store) DueVideoJobs(ctx context.Context, now time.Time, limit int) ([]domain.VideoJob, error) {
	rows, err := s.readDB.QueryContext(ctx, "SELECT "+videoJobColumns+" FROM video_jobs WHERE status='pending' AND next_poll_at<=? ORDER BY next_poll_at LIMIT ?", millis(now), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := []domain.VideoJob{}
	for rows.Next() {
		job, err := scanVideoJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

// ExpireStaleVideoJobs ends jobs that nobody can finish any more.
func (s *Store) ExpireStaleVideoJobs(ctx context.Context, pendingBefore, dispatchingBefore, now time.Time) (int, error) {
	return s.writeDeleteCount(ctx, `UPDATE video_jobs SET status='unknown',error_code='video_result_unknown',finished_at=?,next_poll_at=NULL
 WHERE (status='pending' AND created_at<?) OR (status='dispatching' AND created_at<?)`, millis(now), millis(pendingBefore), millis(dispatchingBefore))
}

// PruneVideoJobs deletes old finished rows in a bounded batch.
func (s *Store) PruneVideoJobs(ctx context.Context, before time.Time, limit int) (int, error) {
	return s.writeDeleteCount(ctx, `DELETE FROM video_jobs WHERE id IN (SELECT id FROM video_jobs WHERE finished_at IS NOT NULL AND finished_at<? LIMIT ?)`, millis(before), limit)
}

// VideoJobEntries pages the whole pool's jobs for the dashboard; URLs stay out.
func (s *Store) VideoJobEntries(ctx context.Context, before *domain.VideoJobCursor, limit int) ([]domain.VideoJobEntry, error) {
	var query strings.Builder
	query.WriteString(`SELECT j.id,coalesce(k.name,''),coalesce(nullif(a.label,''),a.email,''),j.model,j.mode,j.requested_seconds,j.status,
 j.duration_seconds,j.cost_microdollars,j.error_code,j.created_at,j.finished_at
 FROM video_jobs j LEFT JOIN api_keys k ON k.id=j.api_key_id LEFT JOIN video_accounts a ON a.id=j.account_id WHERE 1=1`)
	args := []any{}
	if before != nil {
		query.WriteString(" AND (j.created_at,j.id)<(?,?)")
		args = append(args, millis(before.CreatedAt), before.ID)
	}
	query.WriteString(" ORDER BY j.created_at DESC,j.id DESC LIMIT ?")
	args = append(args, limit)
	rows, err := s.readDB.QueryContext(ctx, query.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []domain.VideoJobEntry{}
	for rows.Next() {
		var e domain.VideoJobEntry
		var duration sql.NullFloat64
		var cost, finished sql.NullInt64
		var created int64
		if err := rows.Scan(&e.ID, &e.KeyName, &e.AccountLabel, &e.Model, &e.Mode, &e.RequestedSeconds, &e.Status, &duration, &cost, &e.ErrorCode, &created, &finished); err != nil {
			return nil, err
		}
		e.CreatedAt, e.FinishedAt = fromMillis(created), nullableMillisTime(finished)
		if duration.Valid {
			e.DurationSeconds = &duration.Float64
		}
		if cost.Valid {
			e.CostMicrodollars = &cost.Int64
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// VideoTotalsSince sums jobs created since a moment; refused attempts are not
// jobs a client asked twice for, so they stay out.
func (s *Store) VideoTotalsSince(ctx context.Context, since time.Time) (domain.VideoPeriodTotals, error) {
	var totals domain.VideoPeriodTotals
	err := s.readDB.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(status='done'),0),coalesce(sum(duration_seconds),0),coalesce(sum(cost_microdollars),0)
 FROM video_jobs WHERE created_at>=? AND status<>'refused'`, millis(since)).Scan(&totals.Jobs, &totals.Done, &totals.Seconds, &totals.CostMicrodollars)
	return totals, err
}
