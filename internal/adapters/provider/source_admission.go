package provider

import (
	"context"
	"sync"
	"time"

	"codex-lb/internal/domain"
)

// beginSource bounds active provider operations across protocols. Requests do
// not create a second unbounded queue after the shared application admission.
func (a *Adapter) beginSource(ctx context.Context, source domain.ModelSource) (context.Context, func(), error) {
	if err := ctx.Err(); err != nil {
		return ctx, nil, err
	}
	a.sourceMu.Lock()
	if source.MaxConcurrency > 0 && a.sourceActive[source.ID] >= source.MaxConcurrency {
		a.sourceMu.Unlock()
		return ctx, nil, providerFailure("model_source_capacity", 503, false)
	}
	if a.sourceActive == nil {
		a.sourceActive = make(map[string]int)
	}
	a.sourceActive[source.ID]++
	a.sourceMu.Unlock()
	timeout := 10 * time.Minute
	if source.TimeoutSeconds > 0 {
		timeout = time.Duration(source.TimeoutSeconds) * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	release := sync.OnceFunc(func() {
		cancel()
		a.sourceMu.Lock()
		a.sourceActive[source.ID]--
		if a.sourceActive[source.ID] == 0 {
			delete(a.sourceActive, source.ID)
		}
		a.sourceMu.Unlock()
	})
	return ctx, release, nil
}
