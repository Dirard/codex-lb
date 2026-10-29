package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"codex-lb/internal/domain"
)

// KeysBulkRepository is the storage of bulk key actions: each call is one
// atomic write job.
type KeysBulkRepository interface {
	SetAPIKeysActive(context.Context, []string, bool) ([]bool, error)
	MoveAPIKeyToGroup(context.Context, string, *string, time.Time) error
	SetAPIKeyLimits(context.Context, string, []domain.LimitRule, time.Time) error
}

// Validation failures of a bulk request; both wrap domain.ErrInvalid.
var (
	ErrBulkRequest = fmt.Errorf("bulk key request: %w", domain.ErrInvalid)
	ErrBulkLimits  = fmt.Errorf("bulk key limits: %w", domain.ErrInvalidLimit)
)

// KeysBulkActiveJob is the number of keys one disable or enable job changes
// (R37); a move or new limits change one key per job because saving a key
// may backfill its new limit windows inside the write transaction.
const KeysBulkActiveJob = 25

// KeyBulkRequest is a decoded POST /api/keys/bulk body. GroupID is the move
// target (nil for no group); Limits replace the limits, nil only when the
// action is not set_limits.
type KeyBulkRequest struct {
	IDs     []string
	Action  domain.KeyBulkAction
	GroupID *string
	Limits  []domain.LimitRule
}

// KeysBulkService is use case 6.1 of the dashboard redesign: one action over
// many keys in short atomic write jobs with a result per key.
type KeysBulkService struct {
	repo KeysBulkRepository
	now  func() time.Time
}

func NewKeysBulkService(repo KeysBulkRepository, now func() time.Time) *KeysBulkService {
	if now == nil {
		now = time.Now
	}
	return &KeysBulkService{repo: repo, now: now}
}

// validate is the step "validate ids and the action": 1 to 1 000 distinct,
// non-blank ids of at most 256 bytes, a known action, a non-blank target
// group and valid limit rules.
func (r KeyBulkRequest) validate() error {
	if len(r.IDs) == 0 || len(r.IDs) > domain.KeyIDsLimit {
		return fmt.Errorf("%d ids: %w", len(r.IDs), ErrBulkRequest)
	}
	seen := make(map[string]bool, len(r.IDs))
	for _, id := range r.IDs {
		if id == "" || len(id) > keysMaxTextLength || seen[id] {
			return fmt.Errorf("blank, long or repeated id: %w", ErrBulkRequest)
		}
		seen[id] = true
	}
	switch r.Action {
	case domain.KeyBulkDisable, domain.KeyBulkEnable:
	case domain.KeyBulkMoveGroup:
		if r.GroupID != nil && (*r.GroupID == "" || len(*r.GroupID) > keysMaxTextLength) {
			return fmt.Errorf("group id: %w", ErrBulkRequest)
		}
	case domain.KeyBulkSetLimits:
		if r.Limits == nil {
			return fmt.Errorf("limits required: %w", ErrBulkRequest)
		}
		if err := domain.ValidateLimitRules(r.Limits); err != nil {
			return fmt.Errorf("%w: %w", ErrBulkLimits, err)
		}
	default:
		return fmt.Errorf("action %q: %w", r.Action, ErrBulkRequest)
	}
	return nil
}

// Apply is the workflow "apply in short write jobs --> [every key changed,
// some keys failed] --> report per-key results": jobs run in request order,
// a failed job fails only its keys and never undoes earlier jobs, and once
// the request is cancelled the remaining keys are reported not applied.
func (s *KeysBulkService) Apply(ctx context.Context, r KeyBulkRequest) (domain.KeyBulkResponse, error) {
	if err := r.validate(); err != nil {
		return domain.KeyBulkResponse{}, err
	}
	results := make([]domain.KeyBulkResult, len(r.IDs))
	for i, id := range r.IDs {
		results[i] = domain.KeyBulkResult{ID: id, Code: domain.KeyBulkNotApplied}
	}
	switch r.Action {
	case domain.KeyBulkDisable, domain.KeyBulkEnable:
		active := r.Action == domain.KeyBulkEnable
		for from := 0; from < len(r.IDs) && ctx.Err() == nil; from += KeysBulkActiveJob {
			to := min(len(r.IDs), from+KeysBulkActiveJob)
			found, err := s.repo.SetAPIKeysActive(ctx, r.IDs[from:to], active)
			for i := from; i < to; i++ {
				switch {
				case err != nil:
					results[i].Code = bulkFailureCode(err)
				case found[i-from]:
					results[i] = domain.KeyBulkResult{ID: r.IDs[i], OK: true}
				default:
					results[i].Code = domain.KeyBulkNotFound
				}
			}
		}
	default:
		for i, id := range r.IDs {
			if ctx.Err() != nil {
				break
			}
			var err error
			if r.Action == domain.KeyBulkMoveGroup {
				err = s.repo.MoveAPIKeyToGroup(ctx, id, r.GroupID, s.now().UTC())
			} else {
				err = s.repo.SetAPIKeyLimits(ctx, id, r.Limits, s.now().UTC())
			}
			if err == nil {
				results[i] = domain.KeyBulkResult{ID: id, OK: true}
			} else {
				results[i].Code = bulkFailureCode(err)
			}
		}
	}
	return domain.KeyBulkResponse{Results: results}, nil
}

// bulkFailureCode maps a failed job onto the per-key code of R36.
func bulkFailureCode(err error) string {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return domain.KeyBulkNotFound
	case errors.Is(err, domain.ErrKeyInGroup):
		return domain.KeyBulkKeyInGroup
	case errors.Is(err, domain.ErrUnknownGroup):
		return domain.KeyBulkInvalidGroup
	case errors.Is(err, domain.ErrConflict):
		return domain.KeyBulkConflict
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return domain.KeyBulkNotApplied
	}
	return domain.KeyBulkInternal
}
