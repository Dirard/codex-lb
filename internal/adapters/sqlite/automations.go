package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

const automationRunColumns = `id,job_id,cycle_key,slot_key,trigger_kind,status,model,reasoning_effort,
 scheduled_for,started_at,finished_at,account_id,account_generation,attempt_count,error_code,error_message`

func scanAutomationRun(row scanner) (domain.AutomationRun, error) {
	var run domain.AutomationRun
	var scheduled, started, finished sql.NullInt64
	var reasoning, accountID, errorCode, errorMessage sql.NullString
	err := row.Scan(&run.ID, &run.JobID, &run.CycleKey, &run.SlotKey, &run.Trigger, &run.Status,
		&run.Model, &reasoning, &scheduled, &started, &finished, &accountID, &run.AccountGeneration, &run.AttemptCount, &errorCode, &errorMessage)
	if err != nil {
		return run, err
	}
	run.ReasoningEffort = automationStringPtr(reasoning)
	run.AccountID = automationStringPtr(accountID)
	run.ErrorCode = automationStringPtr(errorCode)
	run.ErrorMessage = automationStringPtr(errorMessage)
	run.ScheduledFor = fromMillis(scheduled.Int64)
	run.StartedAt = optionalTime(started)
	run.FinishedAt = optionalTime(finished)
	return run, nil
}

func automationStringPtr(value sql.NullString) *string {
	if !value.Valid || value.String == "" {
		return nil
	}
	return &value.String
}

func (s *Store) SaveAutomationJob(ctx context.Context, job domain.AutomationJob) error {
	if job.ID == "" || job.Name == "" || job.Schedule.Time == "" || job.Model == "" {
		return ErrInvalid
	}
	return transact(ctx, s.db, func(tx *sql.Tx) error {
		for _, id := range job.AccountIDs {
			var exists int
			if err := tx.QueryRowContext(ctx, `SELECT 1 FROM accounts WHERE id=?
 AND NOT(status='deactivated' AND deactivation_reason='deleted')`, id).Scan(&exists); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return ErrInvalid
				}
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO automation_jobs (
 id,name,enabled,include_paused_accounts,account_scope_all,schedule_type,schedule_time,schedule_timezone,
 schedule_threshold_minutes,schedule_days,model,reasoning_effort,prompt,account_ids,created_at,updated_at
 ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
 ON CONFLICT(id) DO UPDATE SET name=excluded.name,enabled=excluded.enabled,
 include_paused_accounts=excluded.include_paused_accounts,account_scope_all=excluded.account_scope_all,
 schedule_type=excluded.schedule_type,schedule_time=excluded.schedule_time,schedule_timezone=excluded.schedule_timezone,
 schedule_threshold_minutes=excluded.schedule_threshold_minutes,schedule_days=excluded.schedule_days,
 model=excluded.model,reasoning_effort=excluded.reasoning_effort,prompt=excluded.prompt,
 account_ids=excluded.account_ids,updated_at=excluded.updated_at`,
			job.ID, job.Name, boolInt(job.Enabled), boolInt(job.IncludePausedAccounts), boolInt(job.AccountScopeAll),
			job.Schedule.Type, job.Schedule.Time, job.Schedule.Timezone, job.Schedule.ThresholdMinutes,
			strings.Join(job.Schedule.Days, ","), job.Model, job.ReasoningEffort, job.Prompt,
			strings.Join(job.AccountIDs, ","), millis(job.CreatedAt), millis(job.UpdatedAt))
		return err
	})
}

func (s *Store) GetAutomationJob(ctx context.Context, id string) (domain.AutomationJob, error) {
	var job domain.AutomationJob
	var days, accountIDs string
	var reasoning sql.NullString
	var created, updated int64
	err := s.readDB.QueryRowContext(ctx, `SELECT id,name,enabled,include_paused_accounts,account_scope_all,
 schedule_type,schedule_time,schedule_timezone,schedule_threshold_minutes,schedule_days,model,reasoning_effort,
 prompt,account_ids,created_at,updated_at FROM automation_jobs WHERE id=?`, id).
		Scan(&job.ID, &job.Name, &job.Enabled, &job.IncludePausedAccounts, &job.AccountScopeAll,
			&job.Schedule.Type, &job.Schedule.Time, &job.Schedule.Timezone, &job.Schedule.ThresholdMinutes,
			&days, &job.Model, &reasoning, &job.Prompt, &accountIDs, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return job, ErrNotFound
	}
	if err != nil {
		return job, err
	}
	job.Schedule.Days = splitCSV(days)
	job.AccountIDs = splitCSV(accountIDs)
	job.ReasoningEffort = automationStringPtr(reasoning)
	job.CreatedAt, job.UpdatedAt = fromMillis(created), fromMillis(updated)
	return job, nil
}

func (s *Store) ListAutomationJobs(ctx context.Context) ([]domain.AutomationJob, error) {
	rows, err := s.readDB.QueryContext(ctx, `SELECT id FROM automation_jobs ORDER BY created_at,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	jobs := make([]domain.AutomationJob, 0, len(ids))
	for _, id := range ids {
		job, err := s.GetAutomationJob(ctx, id)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

func (s *Store) DeleteAutomationJob(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM automation_jobs WHERE id=?", id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	_, _ = s.db.ExecContext(ctx, "DELETE FROM automation_runs WHERE job_id=?", id)
	return nil
}

// EnsureAutomationRun inserts the durable claim row; existing rows win.
func (s *Store) EnsureAutomationRun(ctx context.Context, run domain.AutomationRun) error {
	if run.ID == "" || run.JobID == "" || run.SlotKey == "" || run.CycleKey == "" {
		return ErrInvalid
	}
	if run.AccountGeneration < 0 {
		return ErrInvalid
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO automation_runs (
 id,job_id,cycle_key,slot_key,trigger_kind,status,model,reasoning_effort,scheduled_for,account_id,account_generation
 ) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(slot_key) DO NOTHING`,
		run.ID, run.JobID, run.CycleKey, run.SlotKey, run.Trigger, domain.AutomationPending,
		run.Model, run.ReasoningEffort, millis(run.ScheduledFor), run.AccountID, run.AccountGeneration)
	return err
}

// ClaimDueAutomationRuns atomically moves due pending rows into running for
// this process; only the winner executes them. Stale running rows are never
// returned here: their upstream execution state is unknowable after a crash.
func (s *Store) ClaimDueAutomationRuns(ctx context.Context, dueBefore time.Time) ([]domain.AutomationRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+automationRunColumns+` FROM automation_runs
 WHERE status=? AND scheduled_for<=? LIMIT 256`, domain.AutomationPending, millis(dueBefore))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var candidates []domain.AutomationRun
	for rows.Next() {
		run, err := scanAutomationRun(rows)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, run)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	claimed := make([]domain.AutomationRun, 0, len(candidates))
	now := time.Now().UTC()
	for _, run := range candidates {
		res, err := s.db.ExecContext(ctx, `UPDATE automation_runs SET status=?, started_at=?, attempt_count=attempt_count+1
	 WHERE id=? AND status=? AND scheduled_for<=? AND account_generation=?`,
			domain.AutomationRunning, millis(now), run.ID, domain.AutomationPending, millis(dueBefore), run.AccountGeneration)
		if err != nil {
			return claimed, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return claimed, err
		}
		if n == 0 {
			continue
		}
		run.Status = domain.AutomationRunning
		run.StartedAt = &now
		run.AttemptCount++
		claimed = append(claimed, run)
	}
	return claimed, nil
}

// AbandonStaleAutomationRuns marks running rows whose claim expired as failed
// without replaying them: the upstream synthetic request may already have
// executed before the crash, so re-running could double-spend.
func (s *Store) AbandonStaleAutomationRuns(ctx context.Context, staleBefore time.Time) (int, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE automation_runs
	 SET status=?, finished_at=?, error_code=?, error_message=?
	 WHERE status=? AND started_at<?`,
		domain.AutomationFailed, millis(time.Now().UTC()), "stale_run_abandoned",
		"Run claim expired after a crash; not replayed automatically", domain.AutomationRunning, millis(staleBefore))
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

func (s *Store) CompleteAutomationRun(ctx context.Context, id string, generation int64, status string, finishedAt time.Time, errorCode, errorMessage *string) error {
	if status != domain.AutomationSuccess && status != domain.AutomationFailed || generation < 0 {
		return ErrInvalid
	}
	res, err := s.db.ExecContext(ctx, `UPDATE automation_runs SET status=?, finished_at=?, error_code=?, error_message=?
	 WHERE id=? AND account_generation=? AND status=?`, status, millis(finishedAt), errorCode, errorMessage, id, generation, domain.AutomationRunning)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrConflict
	}
	return err
}

func (s *Store) ListAutomationRuns(ctx context.Context, filter application.AutomationRunFilter) ([]domain.AutomationRun, int, error) {
	where, args := automationRunWhere(filter)
	var total int
	if err := s.readDB.QueryRowContext(ctx, "SELECT count(*) FROM automation_runs"+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit, offset := filter.Limit, filter.Offset
	if limit <= 0 || limit > 1000 {
		limit = 25
	}
	if offset < 0 {
		offset = 0
	}
	query := "SELECT " + automationRunColumns + " FROM automation_runs" + where +
		" ORDER BY scheduled_for DESC, id LIMIT ? OFFSET ?"
	rows, err := s.readDB.QueryContext(ctx, query, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	runs := make([]domain.AutomationRun, 0)
	for rows.Next() {
		run, err := scanAutomationRun(rows)
		if err != nil {
			return nil, 0, err
		}
		runs = append(runs, run)
	}
	return runs, total, rows.Err()
}

func automationRunWhere(filter application.AutomationRunFilter) (string, []any) {
	clauses := []string{"1=1"}
	var args []any
	if len(filter.JobIDs) > 0 {
		clauses = append(clauses, "job_id IN ("+placeholders(len(filter.JobIDs))+")")
		args = append(args, stringsToAny(filter.JobIDs)...)
	}
	if len(filter.AccountIDs) > 0 {
		clauses = append(clauses, "account_id IN ("+placeholders(len(filter.AccountIDs))+")")
		args = append(args, stringsToAny(filter.AccountIDs)...)
	}
	if len(filter.Statuses) > 0 {
		clauses = append(clauses, "status IN ("+placeholders(len(filter.Statuses))+")")
		args = append(args, stringsToAny(filter.Statuses)...)
	}
	if len(filter.Models) > 0 {
		clauses = append(clauses, "model IN ("+placeholders(len(filter.Models))+")")
		args = append(args, stringsToAny(filter.Models)...)
	}
	if len(filter.Triggers) > 0 {
		clauses = append(clauses, "trigger_kind IN ("+placeholders(len(filter.Triggers))+")")
		args = append(args, stringsToAny(filter.Triggers)...)
	}
	if filter.Search != "" {
		clauses = append(clauses, "(model LIKE ? OR job_id LIKE ? OR account_id LIKE ?)")
		needle := "%" + filter.Search + "%"
		args = append(args, needle, needle, needle)
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

func (s *Store) LatestAutomationRunByJob(ctx context.Context) (map[string]domain.AutomationRun, error) {
	rows, err := s.readDB.QueryContext(ctx, `SELECT `+automationRunColumns+` FROM automation_runs r
	 WHERE scheduled_for=(SELECT max(r2.scheduled_for) FROM automation_runs r2 WHERE r2.job_id=r.job_id)
	 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]domain.AutomationRun{}
	for rows.Next() {
		run, err := scanAutomationRun(rows)
		if err != nil {
			return nil, err
		}
		result[run.JobID] = run
	}
	return result, rows.Err()
}

func (s *Store) ListAutomationRunsByCycle(ctx context.Context, cycleKey string) ([]domain.AutomationRun, error) {
	rows, err := s.readDB.QueryContext(ctx, "SELECT "+automationRunColumns+" FROM automation_runs WHERE cycle_key=? ORDER BY scheduled_for,id", cycleKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := make([]domain.AutomationRun, 0)
	for rows.Next() {
		run, err := scanAutomationRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func stringsToAny(values []string) []any {
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = value
	}
	return result
}

func splitCSV(value string) []string {
	if value == "" {
		return []string{}
	}
	return strings.Split(value, ",")
}

func automationStringPtrOrEmpty(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
