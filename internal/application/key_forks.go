package application

import (
	"context"
	"errors"
	"slices"
	"time"

	"codex-lb/internal/domain"
)

// KeyForkStore is what fork management reads and writes. SaveAPIKey checks
// a fork against its parent in the same transaction.
type KeyForkStore interface {
	GetStoredAPIKey(context.Context, string) (domain.APIKey, error)
	ListKeyForks(context.Context, string) ([]domain.APIKey, error)
	SaveAPIKey(context.Context, domain.APIKey, time.Time) error
	DeleteAPIKey(context.Context, string) error
}

// KeyForks lets the holder of a key manage forks of it: child keys that never
// exceed their parent and share its request pool.
type KeyForks struct {
	store KeyForkStore
	now   func() time.Time
}

func NewKeyForks(store KeyForkStore, now func() time.Time) *KeyForks {
	if now == nil {
		now = time.Now
	}
	return &KeyForks{store: store, now: now}
}

func forkManagementForbidden() error {
	return &ProxyError{Code: "fork_management_forbidden", Status: 403, Message: "Only a key that is not a fork can manage forks"}
}

func forkNotFound() error {
	return &ProxyError{Code: "fork_not_found", Status: 404, Message: "This key has no such fork"}
}

// authorize admits a real key that is not a fork.
func authorizeForkParent(parent domain.APIKey) error {
	if parent.ID == "" || domain.IsInternalKey(parent.ID) || parent.Fork != nil {
		return forkManagementForbidden()
	}
	return nil
}

// List returns the parent's forks with their own settings.
func (s *KeyForks) List(ctx context.Context, parent domain.APIKey) ([]domain.APIKey, error) {
	if err := authorizeForkParent(parent); err != nil {
		return nil, err
	}
	return s.store.ListKeyForks(ctx, parent.ID)
}

// Get returns one of the parent's forks.
func (s *KeyForks) Get(ctx context.Context, parent domain.APIKey, id string) (domain.APIKey, error) {
	if err := authorizeForkParent(parent); err != nil {
		return domain.APIKey{}, err
	}
	fork, err := s.store.GetStoredAPIKey(ctx, id)
	if errors.Is(err, domain.ErrNotFound) || err == nil && (fork.Fork == nil || fork.Fork.ParentID != parent.ID) {
		return domain.APIKey{}, forkNotFound()
	}
	return fork, err
}

// Create stores fork, which already carries its id, secret hash and settings,
// as a fork of parent. Restrictions it omits are the parent's.
func (s *KeyForks) Create(ctx context.Context, parent domain.APIKey, fork domain.APIKey) (domain.APIKey, error) {
	if err := authorizeForkParent(parent); err != nil {
		return domain.APIKey{}, err
	}
	if fork.AllowedModels == nil && len(parent.AllowedModels) > 0 {
		fork.AllowedModels = slices.Clone(parent.AllowedModels)
	}
	if fork.AllowedReasoningEfforts == nil && fork.EnforcedReasoningEffort == nil && parent.EnforcedReasoningEffort == nil && parent.AllowedReasoningEfforts != nil {
		fork.AllowedReasoningEfforts = slices.Clone(parent.AllowedReasoningEfforts)
	}
	now := s.now().UTC()
	fork.CreatedAt, fork.LastUsedAt = now, nil
	fork.Fork = &domain.KeyFork{ParentID: parent.ID, CreatedBy: domain.KeyActorOwner, UpdatedBy: domain.KeyActorOwner}
	if err := s.store.SaveAPIKey(ctx, fork, now); err != nil {
		return domain.APIKey{}, err
	}
	return s.store.GetStoredAPIKey(ctx, fork.ID)
}

// Update applies change to one of the parent's forks and stores it.
func (s *KeyForks) Update(ctx context.Context, parent domain.APIKey, id string, change func(*domain.APIKey) error) (domain.APIKey, error) {
	fork, err := s.Get(ctx, parent, id)
	if err != nil {
		return domain.APIKey{}, err
	}
	if err := change(&fork); err != nil {
		return domain.APIKey{}, err
	}
	fork.Fork.UpdatedBy = domain.KeyActorOwner
	if err := s.store.SaveAPIKey(ctx, fork, s.now().UTC()); err != nil {
		return domain.APIKey{}, err
	}
	return s.store.GetStoredAPIKey(ctx, id)
}

// Delete deletes one of the parent's forks.
func (s *KeyForks) Delete(ctx context.Context, parent domain.APIKey, id string) error {
	if _, err := s.Get(ctx, parent, id); err != nil {
		return err
	}
	return s.store.DeleteAPIKey(ctx, id)
}
