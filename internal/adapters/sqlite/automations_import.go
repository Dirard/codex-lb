package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// importLegacyAutomations carries persisted automation jobs and their run
// history from a legacy snapshot. Working schedules survive migration instead
// of collapsing into an empty dashboard. Legacy "running" rows are imported
// as failed/stale_run_abandoned: their upstream execution state is unknowable
// and must not be replayed.
func importLegacyAutomations(ctx context.Context, src, dst *sql.Tx, _ LegacyVault) error {
	exists, err := legacyTableExists(ctx, src, "automation_jobs")
	if err != nil {
		return err
	}
	if !exists {
		// Old snapshots predate automations; absence is not an import failure.
		return nil
	}
	rows, err := src.QueryContext(ctx, `SELECT j.id,j.name,j.enabled,j.include_paused_accounts,j.account_scope_all,
j.schedule_type,j.schedule_time,j.schedule_timezone,j.schedule_threshold_minutes,j.schedule_days,j.model,
j.reasoning_effort,j.prompt,j.created_at,j.updated_at,
COALESCE((SELECT group_concat(a.account_id) FROM (SELECT account_id FROM automation_job_accounts
 WHERE job_id=j.id ORDER BY position) a),'')
FROM automation_jobs j ORDER BY j.created_at,j.id`)
	if err != nil {
		return fmt.Errorf("legacy automation jobs: %w", err)
	}
	defer rows.Close()
	type legacyJob struct {
		id, name, scopeType, time, timezone, days, model, reasoning, prompt, accountIDs string
		enabled, includePaused, scopeAll                                                bool
		threshold                                                                       int
		created, updated                                                                any
	}
	var jobs []legacyJob
	for rows.Next() {
		var job legacyJob
		if err := rows.Scan(&job.id, &job.name, &job.enabled, &job.includePaused, &job.scopeAll,
			&job.scopeType, &job.time, &job.timezone, &job.threshold, &job.days, &job.model,
			&job.reasoning, &job.prompt, &job.created, &job.updated, &job.accountIDs); err != nil {
			return err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, job := range jobs {
		if job.scopeType == "" {
			job.scopeType = "daily"
		}
		if job.days == "" {
			job.days = "mon,tue,wed,thu,fri,sat,sun"
		}
		if job.prompt == "" {
			job.prompt = "ping"
		}
		// An explicit empty target list with scope_all=false stays scoped
		// (targets nothing); widening it to "all" would change semantics.
		scopeAll := job.scopeAll
		created, err := parseLegacyTime(job.created)
		if err != nil {
			return err
		}
		updated, err := parseLegacyTime(job.updated)
		if err != nil {
			return err
		}
		if _, err := dst.ExecContext(ctx, `INSERT INTO automation_jobs (
 id,name,enabled,include_paused_accounts,account_scope_all,schedule_type,schedule_time,schedule_timezone,
 schedule_threshold_minutes,schedule_days,model,reasoning_effort,prompt,account_ids,created_at,updated_at)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			job.id, job.name, boolInt(job.enabled), boolInt(job.includePaused), boolInt(scopeAll),
			job.scopeType, job.time, job.timezone, job.threshold, job.days, job.model,
			automationStringPtrOrEmpty(job.reasoning), job.prompt, job.accountIDs,
			millis(created), millis(updated)); err != nil {
			return err
		}
	}
	if err := importLegacyAutomationRuns(ctx, src, dst); err != nil {
		return err
	}
	return nil
}

func importLegacyAutomationRuns(ctx context.Context, src, dst *sql.Tx) error {
	exists, err := legacyTableExists(ctx, src, "automation_runs")
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	rows, err := src.QueryContext(ctx, `SELECT r.id,r.job_id,r.slot_key,r.cycle_key,r.trigger,r.model,
r.reasoning_effort,r.scheduled_for,r.started_at,r.finished_at,r.status,r.account_id,r.error_code,
r.error_message,r.attempt_count FROM automation_runs r ORDER BY r.scheduled_for,r.id`)
	if err != nil {
		return fmt.Errorf("legacy automation runs: %w", err)
	}
	defer rows.Close()
	var values []func() error
	for rows.Next() {
		var id, jobID, slotKey, cycleKey, trigger, status string
		var model, reasoning sql.NullString
		var accountID, errorCode, errorMessage sql.NullString
		var scheduled, started any
		var finished sql.NullString
		var attempts int
		if err := rows.Scan(&id, &jobID, &slotKey, &cycleKey, &trigger, &model, &reasoning,
			&scheduled, &started, &finished, &status, &accountID, &errorCode, &errorMessage, &attempts); err != nil {
			return err
		}
		values = append(values, func() error {
			scheduledFor, err := parseLegacyTime(scheduled)
			if err != nil {
				return err
			}
			startedAt, err := parseLegacyTime(started)
			if err != nil {
				return err
			}
			var finishedAt any
			if finished.Valid && strings.TrimSpace(finished.String) != "" {
				parsed, err := parseLegacyTime(finished.String)
				if err != nil {
					return err
				}
				finishedAt = millis(parsed)
			}
			// Legacy 'partial' was a cycle-rollup status; the Go model derives
			// rollups on read, so imported partials are recorded as failed with
			// their original error preserved.
			mappedStatus := status
			switch status {
			case "running":
				mappedStatus = "failed"
				errorCode = sql.NullString{String: "stale_run_abandoned", Valid: true}
				errorMessage = sql.NullString{String: "Imported legacy run was still running; not replayed", Valid: true}
			case "partial":
				mappedStatus = "failed"
				if !errorCode.Valid {
					errorCode = sql.NullString{String: "legacy_partial", Valid: true}
				}
			case "pending":
				// The original slot semantics are unknown post-import; never
				// execute an imported pending row as if it were newly due.
				mappedStatus = "failed"
				errorCode = sql.NullString{String: "legacy_pending_imported", Valid: true}
				errorMessage = sql.NullString{String: "Imported pending run was not replayed", Valid: true}
			}
			if mappedStatus == "" {
				mappedStatus = "failed"
			}
			_, err = dst.ExecContext(ctx, `INSERT INTO automation_runs (
 id,job_id,cycle_key,slot_key,trigger_kind,status,model,reasoning_effort,scheduled_for,started_at,
 finished_at,account_id,attempt_count,error_code,error_message)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(slot_key) DO NOTHING`,
				id, jobID, cycleKey, slotKey, trigger, mappedStatus, model.String, reasoning,
				millis(scheduledFor), millis(startedAt), finishedAt, accountID, attempts, errorCode, errorMessage)
			return err
		})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, insert := range values {
		if err := insert(); err != nil {
			return err
		}
	}
	return nil
}

func legacyTableExists(ctx context.Context, tx *sql.Tx, name string) (bool, error) {
	var count int
	if err := tx.QueryRowContext(ctx,
		"SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}
