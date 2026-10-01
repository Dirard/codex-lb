package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// importLegacyWarmupAttempts carries every claimed tuple into the Go ledger.
// The legacy row is committed before provider dispatch, so even a pending or
// failed row may already have consumed quota and must remain deduplicated.
func importLegacyWarmupAttempts(ctx context.Context, src, dst *sql.Tx) error {
	var existing int
	if err := dst.QueryRowContext(ctx, "SELECT count(*) FROM account_limit_warmup_attempts").Scan(&existing); err != nil {
		return err
	}
	if existing != 0 {
		return fmt.Errorf("destination already contains warm-up attempts: %w", ErrConflict)
	}
	var present int
	if err := src.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='account_limit_warmups'`).Scan(&present); err != nil {
		return err
	}
	if present == 0 { // Snapshots predating limit warm-up have nothing to replay.
		return nil
	}
	rows, err := src.QueryContext(ctx, `SELECT id,account_id,window,reset_at,status,model,
 attempted_at,completed_at,error_code FROM account_limit_warmups ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, resetSeconds int64
		var accountID, window, legacyStatus, model string
		var attemptedRaw, completedRaw any
		var errorCode sql.NullString
		if err := rows.Scan(&id, &accountID, &window, &resetSeconds, &legacyStatus, &model,
			&attemptedRaw, &completedRaw, &errorCode); err != nil {
			return err
		}
		status, ok := importedWarmupStatus(legacyStatus)
		resetMillis := time.Unix(resetSeconds, 0).UnixMilli()
		if !ok || id <= 0 || accountID == "" || window == "" || resetSeconds <= 0 || resetMillis/1000 != resetSeconds {
			return fmt.Errorf("legacy warm-up row %d: %w", id, ErrInvalid)
		}
		attemptedAt, err := parseLegacyTime(attemptedRaw)
		if err != nil {
			return fmt.Errorf("legacy warm-up row %d attempted_at: %w", id, err)
		}
		var completedAt any
		if completedRaw != nil {
			finished, err := parseLegacyTime(completedRaw)
			if err != nil {
				return fmt.Errorf("legacy warm-up row %d completed_at: %w", id, err)
			}
			completedAt = millis(finished)
		}
		if _, err := dst.ExecContext(ctx, `INSERT INTO account_limit_warmup_attempts
 (account_id,attempt,status,model,attempted_at,completed_at,error_code,window,reset_at)
 VALUES(?,?,?,?,?,?,?,?,?)`, accountID, id, status, model, millis(attemptedAt),
			completedAt, nullText(errorCode), window, resetMillis); err != nil {
			return fmt.Errorf("import legacy warm-up row %d: %w", id, err)
		}
	}
	return rows.Err()
}

func importedWarmupStatus(status string) (string, bool) {
	switch status {
	case "pending":
		return "claimed", true
	case "succeeded":
		return "success", true
	case "failed":
		return "failed", true
	case "skipped":
		return "abandoned", true
	default:
		return "", false
	}
}
