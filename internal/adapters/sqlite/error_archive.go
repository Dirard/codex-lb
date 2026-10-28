package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"codex-lb/internal/domain"
)

const schemaV13 = `
CREATE TABLE error_archives (
 request_id TEXT PRIMARY KEY,account_id TEXT NOT NULL,key_id TEXT NOT NULL,
 transport TEXT NOT NULL,occurred_at INTEGER NOT NULL,expires_at INTEGER NOT NULL,
 content_encrypted BLOB NOT NULL
);
CREATE INDEX idx_error_archives_time ON error_archives(occurred_at);
CREATE INDEX idx_error_archives_expiry ON error_archives(expires_at);
`

func (s *Store) SaveErrorArchive(ctx context.Context, e domain.ErrorArchive, maxBytes int64) error {
	if e.RequestID == "" || len(e.RequestID) > 256 || len(e.ContentEncrypted) == 0 || len(e.ContentEncrypted) > 1<<20 || !fernetCiphertext(e.ContentEncrypted) || maxBytes < int64(len(e.ContentEncrypted)) || e.OccurredAt.IsZero() || !e.ExpiresAt.After(e.OccurredAt) {
		return ErrInvalid
	}
	return s.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if e.AccountID != "" {
			current, err := accountIncarnationCurrentTx(ctx, tx, e.AccountID, e.AccountGeneration)
			if err != nil || !current {
				return err // A late diagnostic cannot recreate a deleted account's archive.
			}
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM error_archives WHERE expires_at<=?", time.Now().UTC().UnixMilli()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO error_archives(request_id,account_id,key_id,transport,occurred_at,expires_at,content_encrypted) VALUES(?,?,?,?,?,?,?) ON CONFLICT(request_id) DO NOTHING`, e.RequestID, e.AccountID, e.KeyID, e.Transport, millis(e.OccurredAt), millis(e.ExpiresAt), e.ContentEncrypted); err != nil {
			return err
		}
		// The quota covers encrypted payload bytes; record size is independently
		// bounded above. Old diagnostics may expire without touching usage totals.
		return evictErrorArchivesTx(ctx, tx, maxBytes)
	})
}

const maxErrorArchives = 10000

// evictErrorArchivesTx removes the oldest archives until the trigger-maintained
// counters are within maxBytes and maxErrorArchives: the same newest prefix the
// former window function kept, in O(evicted rows).
func evictErrorArchivesTx(ctx context.Context, tx *sql.Tx, maxBytes int64) error {
	var count, bytes int64
	if err := tx.QueryRowContext(ctx, "SELECT archive_count,archive_bytes FROM error_archive_stats WHERE id=1").Scan(&count, &bytes); err != nil {
		return err
	}
	for count > maxErrorArchives || bytes > maxBytes {
		rows, err := tx.QueryContext(ctx, `SELECT request_id,length(content_encrypted) FROM error_archives
 ORDER BY occurred_at,request_id LIMIT 64`)
		if err != nil {
			return err
		}
		type victim struct {
			id   string
			size int64
		}
		var victims []victim
		for rows.Next() {
			var item victim
			if err := rows.Scan(&item.id, &item.size); err != nil {
				rows.Close()
				return err
			}
			victims = append(victims, item)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(victims) == 0 {
			return nil
		}
		for _, item := range victims {
			if count <= maxErrorArchives && bytes <= maxBytes {
				break
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM error_archives WHERE request_id=?", item.id); err != nil {
				return err
			}
			count, bytes = count-1, bytes-item.size
		}
	}
	return nil
}

func (s *Store) QueryErrorArchives(ctx context.Context, f domain.ErrorArchiveFilter, now time.Time) (domain.ErrorArchivePage, error) {
	var page domain.ErrorArchivePage
	if f.Limit < 1 || f.Limit > 200 || f.Offset < 0 {
		return page, ErrInvalid
	}
	conditions := []string{"expires_at>?"}
	args := []any{millis(now)}
	if f.RequestID != "" {
		conditions = append(conditions, "request_id=?")
		args = append(args, f.RequestID)
	}
	if f.Transport != "" {
		conditions = append(conditions, "transport=?")
		args = append(args, f.Transport)
	}
	if f.Start != nil {
		conditions = append(conditions, "occurred_at>=?")
		args = append(args, millis(*f.Start))
	}
	if f.End != nil {
		conditions = append(conditions, "occurred_at<?")
		args = append(args, millis(*f.End))
	}
	where := strings.Join(conditions, " AND ")
	if err := s.readDB.QueryRowContext(ctx, "SELECT count(*) FROM error_archives WHERE "+where, args...).Scan(&page.Total); err != nil {
		return page, err
	}
	rows, err := s.readDB.QueryContext(ctx, "SELECT request_id,account_id,key_id,transport,occurred_at,expires_at,content_encrypted FROM error_archives WHERE "+where+" ORDER BY occurred_at,request_id LIMIT ? OFFSET ?", append(args, f.Limit, f.Offset)...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	page.Items = []domain.ErrorArchive{}
	for rows.Next() {
		var e domain.ErrorArchive
		var at, expires int64
		if err := rows.Scan(&e.RequestID, &e.AccountID, &e.KeyID, &e.Transport, &at, &expires, &e.ContentEncrypted); err != nil {
			return page, err
		}
		e.OccurredAt, e.ExpiresAt = fromMillis(at), fromMillis(expires)
		page.Items = append(page.Items, e)
	}
	return page, rows.Err()
}

func (s *Store) PruneErrorArchives(ctx context.Context, now time.Time) error {
	if now.IsZero() {
		return fmt.Errorf("archive retention time: %w", ErrInvalid)
	}
	_, err := s.exec(ctx, "DELETE FROM error_archives WHERE expires_at<=?", millis(now))
	return err
}

func (s *Store) ListErrorArchiveDays(ctx context.Context, now time.Time) ([]domain.ErrorArchiveDay, error) {
	rows, err := s.readDB.QueryContext(ctx, `SELECT strftime('%Y-%m-%d',occurred_at/1000,'unixepoch'),sum(length(content_encrypted)),max(occurred_at) FROM error_archives WHERE expires_at>? GROUP BY 1 ORDER BY 1 DESC`, millis(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	days := []domain.ErrorArchiveDay{}
	for rows.Next() {
		var day domain.ErrorArchiveDay
		var at int64
		if err := rows.Scan(&day.Day, &day.Bytes, &at); err != nil {
			return nil, err
		}
		day.ModifiedAt = fromMillis(at)
		days = append(days, day)
	}
	return days, rows.Err()
}
