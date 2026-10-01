package sqlite

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"codex-lb/internal/domain"
)

const affinityDisplayName = "coalesce(nullif(a.alias,''),nullif(a.email,''),a.id)"

func affinityFilter(filter domain.AffinityFilter, now time.Time, ttl time.Duration) (string, []any, error) {
	if now.IsZero() || ttl <= 0 || len(filter.AccountQuery) > 512 || len(filter.KeyQuery) > 256 || filter.Kind != "" && !filter.Kind.Valid() {
		return "", nil, ErrInvalid
	}
	where, args := []string{"1=1"}, []any{}
	if filter.Kind != "" {
		where, args = append(where, "b.kind=?"), append(args, filter.Kind)
	}
	if filter.StaleOnly {
		where, args = append(where, "b.kind='prompt_cache' AND b.updated_at<=?"), append(args, millis(now.Add(-ttl)))
	}
	if value := strings.TrimSpace(filter.AccountQuery); value != "" {
		where = append(where, "(instr(lower("+affinityDisplayName+"),lower(?))>0 OR instr(lower(a.id),lower(?))>0)")
		args = append(args, value, value)
	}
	if value := strings.TrimSpace(filter.KeyQuery); value != "" {
		where, args = append(where, "instr(lower(b.key),lower(?))>0"), append(args, value)
	}
	return strings.Join(where, " AND "), args, nil
}

func (s *Store) ListAffinities(ctx context.Context, filter domain.AffinityFilter, now time.Time, ttl time.Duration) (domain.AffinityList, error) {
	page := domain.AffinityList{Entries: []domain.AffinityEntry{}}
	where, args, err := affinityFilter(filter, now, ttl)
	if err != nil || filter.Limit < 1 || filter.Limit > 500 || filter.Offset < 0 {
		return page, ErrInvalid
	}
	order := map[string]string{"": "b.updated_at", "updated_at": "b.updated_at", "created_at": "b.created_at", "key": "b.key", "account": affinityDisplayName}[filter.SortBy]
	if order == "" || filter.SortDir != "" && filter.SortDir != "asc" && filter.SortDir != "desc" {
		return page, ErrInvalid
	}
	direction := filter.SortDir
	if direction == "" {
		direction = "desc"
	}
	err = transact(ctx, s.db, func(tx *sql.Tx) error {
		if filter.Kind == "" || filter.Kind == domain.AffinityPromptCache {
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM affinity_bindings WHERE kind='prompt_cache' AND updated_at<=?", millis(now.Add(-ttl))).Scan(&page.StalePromptCacheCount); err != nil {
				return err
			}
		}
		from := " FROM affinity_bindings b JOIN accounts a ON a.id=b.account_id WHERE " + where
		if err := tx.QueryRowContext(ctx, "SELECT count(*)"+from, args...).Scan(&page.Total); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT b.key,b.kind,b.account_id,`+affinityDisplayName+`,b.created_at,b.updated_at`+
			from+" ORDER BY "+order+" "+direction+",b.kind,b.key LIMIT ? OFFSET ?", append(args, filter.Limit, filter.Offset)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var entry domain.AffinityEntry
			var created, updated int64
			if err := rows.Scan(&entry.Key, &entry.Kind, &entry.AccountID, &entry.DisplayName, &created, &updated); err != nil {
				return err
			}
			entry.CreatedAt, entry.UpdatedAt = fromMillis(created), fromMillis(updated)
			if entry.Kind == domain.AffinityPromptCache {
				expires := entry.UpdatedAt.Add(ttl)
				entry.ExpiresAt, entry.IsStale = &expires, !expires.After(now)
			}
			page.Entries = append(page.Entries, entry)
		}
		return rows.Err()
	})
	page.HasMore = filter.Offset+len(page.Entries) < page.Total
	return page, err
}

func (s *Store) DeleteFilteredAffinities(ctx context.Context, filter domain.AffinityFilter, now time.Time, ttl time.Duration) (int, error) {
	where, args, err := affinityFilter(filter, now, ttl)
	if err != nil {
		return 0, err
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM affinity_bindings WHERE (kind,key) IN
 (SELECT b.kind,b.key FROM affinity_bindings b JOIN accounts a ON a.id=b.account_id WHERE `+where+")", args...)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	return int(count), err
}
