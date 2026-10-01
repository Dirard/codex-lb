package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"codex-lb/internal/domain"
)

func (s *Store) SaveModelSource(ctx context.Context, source domain.ModelSource, credential *domain.AccountCredential) error {
	if err := source.Validate(); err != nil {
		return err
	}
	if credential != nil && (credential.AccountID != source.ID || !fernetCiphertext(credential.ExternalKeyEncrypted)) {
		return fmt.Errorf("external credential: %w", ErrInvalid)
	}
	config, err := json.Marshal(source.ProviderConfig)
	if err != nil {
		return err
	}
	if source.CreatedAt.IsZero() {
		source.CreatedAt = time.Now().UTC()
	}
	if source.UpdatedAt.IsZero() {
		source.UpdatedAt = time.Now().UTC()
	}
	return transact(ctx, s.db, func(tx *sql.Tx) error {
		var oldKind, oldURL string
		var oldEnabled, oldChat, oldResponses bool
		err := tx.QueryRowContext(ctx, `SELECT kind,base_url,is_enabled,supports_chat_completions,supports_responses
 FROM legacy_model_sources WHERE id=? AND deleted_at IS NULL`, source.ID).Scan(&oldKind, &oldURL, &oldEnabled, &oldChat, &oldResponses)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		routeChanged := err == nil && (oldKind != string(source.Kind) || oldURL != source.BaseURL || oldChat != source.Chat || oldResponses != source.Responses)
		enabledTransition := err == nil && !oldEnabled && source.Enabled
		if routeChanged {
			for _, table := range []string{"continuations", "codex_resource_owners", "affinity_bindings"} {
				if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE account_id=?", source.ID); err != nil {
					return err
				}
			}
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO legacy_model_sources
 (id,name,kind,base_url,is_enabled,health_status,supports_chat_completions,
 supports_responses,supports_audio_transcriptions,supports_embeddings,timeout_seconds,
 max_concurrency,provider_config_json,created_at,updated_at)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET
 name=excluded.name,kind=excluded.kind,base_url=excluded.base_url,
 is_enabled=excluded.is_enabled,health_status=excluded.health_status,
 supports_chat_completions=excluded.supports_chat_completions,
 supports_responses=excluded.supports_responses,
 supports_audio_transcriptions=excluded.supports_audio_transcriptions,
 supports_embeddings=excluded.supports_embeddings,timeout_seconds=excluded.timeout_seconds,
 max_concurrency=excluded.max_concurrency,provider_config_json=excluded.provider_config_json,
 updated_at=excluded.updated_at WHERE legacy_model_sources.deleted_at IS NULL`,
			source.ID, source.Name, source.Kind, source.BaseURL, boolInt(source.Enabled), source.Health,
			boolInt(source.Chat), boolInt(source.Responses), boolInt(source.Audio), boolInt(source.Embeddings),
			nullPositive(source.TimeoutSeconds), nullPositive(source.MaxConcurrency), string(config),
			millis(source.CreatedAt), millis(source.UpdatedAt))
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
		status := "paused"
		if source.Enabled {
			status = "active"
		}
		res, err = tx.ExecContext(ctx, `INSERT INTO accounts
 (id,kind,provider,base_url,email,alias,plan_type,routing_policy,status,created_at)
 VALUES(?,'external',?,?,?,?, 'external','normal',?,?) ON CONFLICT(id) DO UPDATE SET
 provider=excluded.provider,base_url=excluded.base_url,alias=excluded.alias,
 status=CASE WHEN excluded.status='paused' THEN 'paused'
 WHEN ? AND accounts.status='paused' THEN 'active'
 WHEN ? AND accounts.status IN ('rate_limited','quota_exceeded') THEN 'active'
 ELSE accounts.status END,
 route_revision=accounts.route_revision+?
 WHERE accounts.kind='external' AND NOT(accounts.status='deactivated' AND accounts.deactivation_reason='deleted')
 AND (?=0 OR accounts.route_revision<9223372036854775807)`, source.ID, source.Kind, source.BaseURL, source.ID, source.Name,
			status, millis(source.CreatedAt), boolInt(enabledTransition), boolInt(routeChanged), boolInt(routeChanged), boolInt(routeChanged))
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrConflict
		}
		if credential != nil {
			if _, err := tx.ExecContext(ctx, `INSERT INTO account_credentials(account_id,external_key_encrypted)
 VALUES(?,?) ON CONFLICT(account_id) DO UPDATE SET external_key_encrypted=excluded.external_key_encrypted`,
				source.ID, nullBytes(credential.ExternalKeyEncrypted)); err != nil {
				return err
			}
		} else {
			if _, err := tx.ExecContext(ctx, `INSERT INTO account_credentials(account_id)
 VALUES(?) ON CONFLICT(account_id) DO NOTHING`, source.ID); err != nil {
				return err
			}
		}
		ids := make([]int64, 0, len(source.Models))
		for _, model := range source.Models {
			aliases, err := json.Marshal(model.Aliases)
			if err != nil {
				return err
			}
			if model.CreatedAt.IsZero() {
				model.CreatedAt = source.CreatedAt
			}
			if model.UpdatedAt.IsZero() {
				model.UpdatedAt = source.UpdatedAt
			}
			var id int64
			err = tx.QueryRowContext(ctx, `INSERT INTO legacy_model_source_models
 (source_id,model,display_name,context_window,max_output_tokens,supports_streaming,
 supports_tools,supports_vision,input_per_1m,cached_input_per_1m,output_per_1m,
 audio_per_minute,raw_metadata_json,is_enabled,aliases_json,upstream_model,created_at,updated_at)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
 ON CONFLICT(source_id,model) DO UPDATE SET display_name=excluded.display_name,
 context_window=excluded.context_window,max_output_tokens=excluded.max_output_tokens,
 supports_streaming=excluded.supports_streaming,supports_tools=excluded.supports_tools,
 supports_vision=excluded.supports_vision,input_per_1m=excluded.input_per_1m,
 cached_input_per_1m=excluded.cached_input_per_1m,output_per_1m=excluded.output_per_1m,
 audio_per_minute=excluded.audio_per_minute,raw_metadata_json=excluded.raw_metadata_json,
 is_enabled=excluded.is_enabled,aliases_json=excluded.aliases_json,
 upstream_model=excluded.upstream_model,updated_at=excluded.updated_at
 RETURNING id`, source.ID, model.Model, nullIfEmpty(model.DisplayName), nullPositive64(model.ContextWindow),
				nullPositive64(model.MaxOutputTokens), boolInt(model.Streaming), boolInt(model.Tools), boolInt(model.Vision),
				optionalFloat(model.InputPerMillion), optionalFloat(model.CachedPerMillion),
				optionalFloat(model.OutputPerMillion), optionalFloat(model.AudioPerMinute),
				nullIfEmpty(model.RawMetadataJSON), boolInt(model.Enabled), string(aliases), model.UpstreamModel,
				millis(model.CreatedAt), millis(model.UpdatedAt)).Scan(&id)
			if err != nil {
				return err
			}
			ids = append(ids, id)
		}
		query := "DELETE FROM legacy_model_source_models WHERE source_id=?"
		args := []any{source.ID}
		if len(ids) != 0 {
			query += " AND id NOT IN (" + strings.TrimRight(strings.Repeat("?,", len(ids)), ",") + ")"
			for _, id := range ids {
				args = append(args, id)
			}
		}
		_, err = tx.ExecContext(ctx, query, args...)
		return err
	})
}

func (s *Store) GetModelSource(ctx context.Context, id string) (domain.ModelSource, error) {
	var source domain.ModelSource
	var timeout, concurrency sql.NullInt64
	var config string
	var created, updated int64
	err := s.readDB.QueryRowContext(ctx, `SELECT id,name,kind,base_url,is_enabled,health_status,
 supports_chat_completions,supports_responses,supports_audio_transcriptions,supports_embeddings,
 timeout_seconds,max_concurrency,provider_config_json,created_at,updated_at
 FROM legacy_model_sources WHERE id=? AND deleted_at IS NULL`, id).Scan(&source.ID, &source.Name,
		&source.Kind, &source.BaseURL, &source.Enabled, &source.Health, &source.Chat, &source.Responses,
		&source.Audio, &source.Embeddings, &timeout, &concurrency, &config, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return source, ErrNotFound
	}
	if err != nil {
		return source, err
	}
	if timeout.Valid {
		source.TimeoutSeconds = int(timeout.Int64)
	}
	if concurrency.Valid {
		source.MaxConcurrency = int(concurrency.Int64)
	}
	if err := json.Unmarshal([]byte(config), &source.ProviderConfig); err != nil {
		return source, err
	}
	source.CreatedAt, source.UpdatedAt = fromMillis(created), fromMillis(updated)
	rows, err := s.readDB.QueryContext(ctx, `SELECT id,model,display_name,context_window,max_output_tokens,
 supports_streaming,supports_tools,supports_vision,input_per_1m,cached_input_per_1m,
 output_per_1m,audio_per_minute,raw_metadata_json,is_enabled,aliases_json,upstream_model,
 created_at,updated_at FROM legacy_model_source_models WHERE source_id=? ORDER BY id`, id)
	if err != nil {
		return source, err
	}
	defer rows.Close()
	source.Models = make([]domain.ModelSourceModel, 0)
	for rows.Next() {
		var model domain.ModelSourceModel
		var display, rawMeta sql.NullString
		var contextWindow, maxOutput sql.NullInt64
		var input, cached, output, audio sql.NullFloat64
		var aliases string
		var created, updated int64
		if err := rows.Scan(&model.ID, &model.Model, &display, &contextWindow, &maxOutput,
			&model.Streaming, &model.Tools, &model.Vision, &input, &cached, &output, &audio,
			&rawMeta, &model.Enabled, &aliases, &model.UpstreamModel, &created, &updated); err != nil {
			return source, err
		}
		model.SourceID = id
		model.DisplayName = textVal(display)
		model.RawMetadataJSON = textVal(rawMeta)
		if contextWindow.Valid {
			model.ContextWindow = contextWindow.Int64
		}
		if maxOutput.Valid {
			model.MaxOutputTokens = maxOutput.Int64
		}
		model.InputPerMillion = floatPtr(input)
		model.CachedPerMillion = floatPtr(cached)
		model.OutputPerMillion = floatPtr(output)
		model.AudioPerMinute = floatPtr(audio)
		if err := json.Unmarshal([]byte(aliases), &model.Aliases); err != nil {
			return source, err
		}
		model.CreatedAt, model.UpdatedAt = fromMillis(created), fromMillis(updated)
		source.Models = append(source.Models, model)
	}
	return source, rows.Err()
}

func (s *Store) ListModelSources(ctx context.Context) ([]domain.ModelSource, error) {
	rows, err := s.readDB.QueryContext(ctx, `SELECT id FROM legacy_model_sources WHERE deleted_at IS NULL ORDER BY name,id`)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := make([]domain.ModelSource, 0, len(ids))
	for _, id := range ids {
		source, err := s.GetModelSource(ctx, id)
		if err != nil {
			return nil, err
		}
		result = append(result, source)
	}
	return result, nil
}

func (s *Store) DeleteModelSource(ctx context.Context, id string) error {
	return transact(ctx, s.db, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE legacy_model_sources SET is_enabled=0,deleted_at=?,updated_at=?
 WHERE id=? AND deleted_at IS NULL`, time.Now().UTC().UnixMilli(), time.Now().UTC().UnixMilli(), id)
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
		return markAccountDeletedTx(ctx, tx, id, false)
	})
}

func nullPositive(v int) any {
	if v == 0 {
		return nil
	}
	return v
}
func nullPositive64(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}
func optionalFloat(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}
func floatPtr(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	return &v.Float64
}
