package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

const schemaV14 = `
CREATE TABLE model_registry_snapshots (
 id INTEGER PRIMARY KEY CHECK(id=1),
 schema_version INTEGER NOT NULL,
 payload TEXT NOT NULL,
 content_hash TEXT NOT NULL,
 refreshed_at INTEGER NOT NULL
);
`

func (s *Store) LoadModelCatalogSnapshot(ctx context.Context) (domain.ModelCatalogRecord, error) {
	var payload, hash string
	var version int64
	var refreshed int64
	err := s.readDB.QueryRowContext(ctx, `SELECT schema_version,payload,content_hash,refreshed_at
 FROM model_registry_snapshots WHERE id=1`).Scan(&version, &payload, &hash, &refreshed)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ModelCatalogRecord{}, nil
	}
	if err != nil {
		return domain.ModelCatalogRecord{}, err
	}
	record := domain.ModelCatalogRecord{SchemaVersion: int(version), ContentHash: hash, RefreshedAt: fromMillis(refreshed)}
	if payload != "null" && payload != "" {
		var snapshot domain.CatalogSnapshot
		if err := json.Unmarshal([]byte(payload), &snapshot); err != nil {
			return domain.ModelCatalogRecord{}, nil
		}
		record.Snapshot = &snapshot
	}
	return record, nil
}

func (s *Store) SaveModelCatalogSnapshot(ctx context.Context, record domain.ModelCatalogRecord, expected ...domain.Account) error {
	if record.SchemaVersion != application.ModelCatalogSchemaVersion || record.RefreshedAt.IsZero() || record.ContentHash == "" {
		return ErrInvalid
	}
	payload, err := json.Marshal(record.Snapshot)
	if err != nil {
		return err
	}
	return transact(ctx, s.db, func(tx *sql.Tx) error {
		for _, account := range expected {
			if account.ID == "" || account.Generation < 0 {
				return ErrInvalid
			}
			var generation int64
			var status domain.AccountStatus
			var reason string
			err := tx.QueryRowContext(ctx, "SELECT generation,status,deactivation_reason FROM accounts WHERE id=?", account.ID).Scan(&generation, &status, &reason)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			if err != nil {
				return err
			}
			if generation != account.Generation || status == domain.AccountDeactivated && reason == "deleted" {
				return ErrConflict
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO model_registry_snapshots
 (id,schema_version,payload,content_hash,refreshed_at) VALUES(1,?,?,?,?)
 ON CONFLICT(id) DO UPDATE SET schema_version=excluded.schema_version,payload=excluded.payload,
 content_hash=excluded.content_hash,refreshed_at=excluded.refreshed_at`,
			record.SchemaVersion, string(payload), record.ContentHash, millis(record.RefreshedAt))
		return err
	})
}
