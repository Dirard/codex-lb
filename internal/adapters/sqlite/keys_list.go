package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"codex-lb/internal/domain"
)

// keyListMaxDays bounds the local days of one keys list read.
const keyListMaxDays = 32

// KeyList reads the keys list (dashboard redesign use case 6) in one analytics
// snapshot: the groups, the non-deleted, non-internal keys with their active
// limits (expired windows projected to zero without writes) and the per-key
// estimated cost of each local day of q. With usable rollups the costs come
// from the report reader of KeyUsageReport (key slices, folded legacy hours,
// the journal, the unfolded tail and raw partial hours) in a fixed number of
// statements; otherwise only today is evaluated from raw events, like the
// overview's fallback. EntitiesOnly reads the groups and keys alone.
func (s *Store) KeyList(ctx context.Context, q domain.KeyListQuery) (domain.KeyListData, error) {
	if q.EntitiesOnly && !q.Now.IsZero() {
		var data domain.KeyListData
		err := s.readSnapshot(ctx, func(tx *sql.Tx) error {
			groups, err := listGroupRefs(ctx, tx)
			if err != nil {
				return err
			}
			keys, err := listKeyRows(ctx, tx, q.Now)
			data = domain.KeyListData{Groups: groups, Keys: keys, Costs: map[string][]float64{}}
			return err
		})
		return data, err
	}
	n := len(q.DayStarts)
	if n == 0 || n > keyListMaxDays || q.Now.IsZero() || !q.DayStarts[n-1].Before(q.End) || q.DayStarts[n-1].After(q.Now) {
		return domain.KeyListData{}, fmt.Errorf("key list query: %w", ErrInvalid)
	}
	query := domain.KeyUsageQuery{DayStarts: q.DayStarts, End: q.End, Now: q.Now, SkipPrevious: true}
	if err := checkedKeyUsageQuery(query); err != nil {
		return domain.KeyListData{}, err
	}
	return analyticsRead(ctx, s, func(r *rollupRead) (domain.KeyListData, error) {
		sums, err := newKeyUsageSums(ctx, r.tx, query)
		if err != nil {
			return domain.KeyListData{}, err
		}
		if err := r.keyUsage(ctx, sums); err != nil {
			return domain.KeyListData{}, err
		}
		return keyListData(sums.result(true), true), nil
	}, func(tx *sql.Tx) (domain.KeyListData, error) {
		today := query
		today.DayStarts = q.DayStarts[n-1:]
		sums, err := newKeyUsageSums(ctx, tx, today)
		if err != nil {
			return domain.KeyListData{}, err
		}
		if err := sums.readEvents(ctx, tx); err != nil {
			return domain.KeyListData{}, err
		}
		return keyListData(sums.result(false), false), nil
	})
}

func keyListData(data domain.KeyUsageData, daysKnown bool) domain.KeyListData {
	result := domain.KeyListData{Groups: data.Groups, Keys: data.Keys, Costs: make(map[string][]float64, len(data.Days)),
		DaysKnown: daysKnown}
	for id, days := range data.Days {
		costs := make([]float64, len(days))
		for i, day := range days {
			costs[i] = day.CostUSD
		}
		result.Costs[id] = costs
	}
	return result
}
