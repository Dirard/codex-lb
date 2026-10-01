package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
	"codex-lb/internal/domain/pricing"
)

const schemaV18 = `
CREATE TABLE codex_price_overrides (
 model TEXT PRIMARY KEY CHECK(length(model) BETWEEN 1 AND 128 AND model=lower(model)),
 price_json TEXT NOT NULL,
 updated_at INTEGER NOT NULL
);
CREATE INDEX idx_usage_events_model_request ON usage_events(model,request_id);
CREATE INDEX idx_legacy_hourly_model ON legacy_hourly_usage(model);
`

const schemaV19 = `
ALTER TABLE api_key_limits ADD COLUMN backfill_from INTEGER;
ALTER TABLE api_key_limits ADD COLUMN backfill_until INTEGER;
`

func decodeModelPrice(raw string) (pricing.Price, error) {
	var price pricing.Price
	if len(raw) == 0 || raw[0] != '{' || json.Unmarshal([]byte(raw), &price) != nil || price.Validate() != nil {
		return pricing.Price{}, fmt.Errorf("invalid persisted model price: %w", ErrInvalid)
	}
	return price, nil
}

func (s *Store) ListModelPriceOverrides(ctx context.Context) (map[string]pricing.Price, error) {
	rows, err := s.readDB.QueryContext(ctx, `SELECT model,price_json FROM codex_price_overrides ORDER BY model`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	prices := map[string]pricing.Price{}
	for rows.Next() {
		var model, raw string
		if err := rows.Scan(&model, &raw); err != nil {
			return nil, err
		}
		price, err := decodeModelPrice(raw)
		if err != nil {
			return nil, err
		}
		prices[model] = price
	}
	return prices, rows.Err()
}

func (s *Store) GetModelPriceOverride(ctx context.Context, model string) (pricing.Price, bool, error) {
	var raw string
	err := s.readDB.QueryRowContext(ctx, `SELECT price_json FROM codex_price_overrides WHERE model=?`, model).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return pricing.Price{}, false, nil
	}
	if err != nil {
		return pricing.Price{}, false, err
	}
	price, err := decodeModelPrice(raw)
	return price, err == nil, err
}

func (s *Store) ApplyModelPriceChange(ctx context.Context, model string, override *pricing.Price, now time.Time) (application.ModelRepriceSummary, error) {
	var summary application.ModelRepriceSummary
	if model == "" || len(model) > 128 || now.IsZero() || override != nil && override.Validate() != nil {
		return summary, ErrInvalid
	}
	err := transact(ctx, s.db, func(tx *sql.Tx) error {
		_, existed, err := modelPriceOverrideTx(ctx, tx, model)
		if err != nil {
			return err
		}
		if override == nil && !existed {
			return nil
		}
		if override != nil {
			raw, err := json.Marshal(override)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO codex_price_overrides(model,price_json,updated_at)
 VALUES(?,?,?) ON CONFLICT(model) DO UPDATE SET price_json=excluded.price_json,
 updated_at=excluded.updated_at`, model, string(raw), millis(now)); err != nil {
				return err
			}
		} else if _, err := tx.ExecContext(ctx, `DELETE FROM codex_price_overrides WHERE model=?`, model); err != nil {
			return err
		}
		_, priced, err := resolveCodexPriceTx(ctx, tx, model)
		if err != nil || !priced {
			// A custom-only deletion removes future pricing, not old ledger sums.
			return err
		}
		summary.Applied = true
		return repriceModelHistoryTx(ctx, tx, model, &summary)
	})
	if err != nil {
		return application.ModelRepriceSummary{}, err
	}
	return summary, nil
}

func modelPriceOverrideTx(ctx context.Context, tx *sql.Tx, model string) (pricing.Price, bool, error) {
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT price_json FROM codex_price_overrides WHERE model=?`, model).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return pricing.Price{}, false, nil
	}
	if err != nil {
		return pricing.Price{}, false, err
	}
	price, err := decodeModelPrice(raw)
	return price, err == nil, err
}

func resolveCodexPriceTx(ctx context.Context, tx *sql.Tx, model string) (pricing.Price, bool, error) {
	model = strings.ToLower(model)
	if custom, ok, err := modelPriceOverrideTx(ctx, tx, model); err != nil || ok {
		return custom, ok, err
	}
	canonical, builtin, err := pricing.LookupCodex(model)
	if errors.Is(err, pricing.ErrUnpriced) {
		return pricing.Price{}, false, nil
	}
	if err != nil {
		return pricing.Price{}, false, err
	}
	if canonical != model {
		if custom, ok, err := modelPriceOverrideTx(ctx, tx, canonical); err != nil || ok {
			return custom, ok, err
		}
	}
	return builtin, true, nil
}

// A tariff edit and settlement are serialized by SQLite. Recheck price inside
// the settlement transaction so an admitted request cannot write stale cost.
func priceUsageEventTx(ctx context.Context, tx *sql.Tx, event *domain.UsageEvent) error {
	if event.AccountID == "" || event.ModelSourceID != "" ||
		event.RequestKind == "transcription" || event.RequestKind == "reconciled" {
		return nil
	}
	if event.Usage.InputTokens == 0 && event.Usage.OutputTokens == 0 && event.Usage.CostMicrodollars > 0 {
		return nil // A non-token charge cannot be derived from token rates.
	}
	var kind domain.AccountKind
	err := tx.QueryRowContext(ctx, `SELECT kind FROM accounts WHERE id=?`, event.AccountID).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // Unattributed diagnostic events retain their recorded cost.
	}
	if err != nil || kind != domain.AccountChatGPT {
		return err
	}
	price, priced, err := resolveCodexPriceTx(ctx, tx, event.Model)
	if err != nil || !priced {
		// A deleted custom-only tariff does not erase the admission snapshot.
		return err
	}
	event.Usage.CostMicrodollars, err = price.Cost(event.Usage, event.ServiceTier)
	return err
}
