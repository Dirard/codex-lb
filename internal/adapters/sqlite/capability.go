package sqlite

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"codex-lb/internal/domain"
)

const schemaV17 = `
CREATE TABLE capability_lineage_markers (
 marker_hash TEXT PRIMARY KEY,
 created_at INTEGER NOT NULL,
 last_seen_at INTEGER NOT NULL
);`

func (s *Store) IsCapabilityRequired(ctx context.Context, capability, keyScope string, aliases []domain.CapabilityLineageAlias) (bool, error) {
	hashes, err := domain.CapabilityLineageMarkerHashes(capability, keyScope, aliases)
	if err != nil {
		return false, err
	}
	if len(hashes) == 0 {
		return false, nil
	}
	placeholders := strings.Repeat("?,", len(hashes))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(hashes))
	for i, hash := range hashes {
		args[i] = hash
	}
	var exists int
	err = s.readDB.QueryRowContext(ctx, "SELECT 1 FROM capability_lineage_markers WHERE marker_hash IN ("+placeholders+") LIMIT 1", args...).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *Store) RequireCapability(ctx context.Context, capability, keyScope string, aliases []domain.CapabilityLineageAlias) ([]string, error) {
	hashes, err := domain.CapabilityLineageMarkerHashes(capability, keyScope, aliases)
	if err != nil || len(hashes) == 0 {
		return hashes, err
	}
	now := time.Now().UTC()
	err = transact(ctx, s.db, func(tx *sql.Tx) error {
		for _, hash := range hashes {
			if _, err := tx.ExecContext(ctx, `INSERT INTO capability_lineage_markers
 (marker_hash,created_at,last_seen_at) VALUES(?,?,?) ON CONFLICT(marker_hash) DO UPDATE SET
 last_seen_at=excluded.last_seen_at`, hash, millis(now), millis(now)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return hashes, nil
}

func importLegacyCapabilityLineage(ctx context.Context, src, dst *sql.Tx) error {
	var count int
	if err := dst.QueryRowContext(ctx, "SELECT count(*) FROM capability_lineage_markers").Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return fmt.Errorf("destination already contains capability lineage markers: %w", ErrConflict)
	}
	var table string
	err := src.QueryRowContext(ctx, "SELECT name FROM sqlite_master WHERE type='table' AND name='capability_lineage_markers'").Scan(&table)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	rows, err := src.QueryContext(ctx, "SELECT marker_hash,created_at,last_seen_at FROM capability_lineage_markers ORDER BY marker_hash")
	if err != nil {
		return err
	}
	defer rows.Close()
	type marker struct {
		hash      string
		createdAt time.Time
		lastSeen  time.Time
	}
	markers := make([]marker, 0)
	for rows.Next() {
		var item marker
		var created, lastSeen any
		if err := rows.Scan(&item.hash, &created, &lastSeen); err != nil {
			return err
		}
		if len(item.hash) != 64 {
			return fmt.Errorf("legacy capability lineage marker has invalid hash: %w", ErrInvalid)
		}
		if _, err := hex.DecodeString(item.hash); err != nil {
			return fmt.Errorf("legacy capability lineage marker has invalid hash: %w", ErrInvalid)
		}
		if item.createdAt, err = parseLegacyTime(created); err != nil {
			return err
		}
		if item.lastSeen, err = parseLegacyTime(lastSeen); err != nil {
			return err
		}
		markers = append(markers, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, item := range markers {
		if _, err := dst.ExecContext(ctx, `INSERT INTO capability_lineage_markers
 (marker_hash,created_at,last_seen_at) VALUES(?,?,?) ON CONFLICT(marker_hash) DO NOTHING`,
			item.hash, millis(item.createdAt), millis(item.lastSeen)); err != nil {
			return err
		}
	}
	return nil
}
