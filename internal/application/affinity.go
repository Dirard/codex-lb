package application

import (
	"context"
	"time"

	"codex-lb/internal/domain"
)

// AffinityRoutingStore is the narrow persistence boundary for soft routing hints.
type AffinityRoutingStore interface {
	LookupAffinity(context.Context, string, domain.AffinityKind, string) (domain.AffinityBinding, error)
	SaveAffinity(context.Context, domain.AffinityBinding, int64, string) (bool, error)
}

// AffinityRepository separates optional locality from correctness ownership.
type AffinityRepository interface {
	AffinityRoutingStore
	ListAffinities(context.Context, domain.AffinityFilter, time.Time, time.Duration) (domain.AffinityList, error)
	DeleteAffinities(context.Context, []domain.AffinityIdentifier) (domain.AffinityDeleteResult, error)
	DeleteFilteredAffinities(context.Context, domain.AffinityFilter, time.Time, time.Duration) (int, error)
	PruneAffinities(context.Context, time.Time, time.Duration, int) (int, error)
}
