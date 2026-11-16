package application

import (
	"context"
	"slices"
	"sync"
	"time"

	"codex-lb/internal/domain"
)

// RequestPools admits the model requests of API keys that have a request
// pool. Each pool is a sliding-window log of admission times with a queue in
// arrival order; a slot frees one domain.RequestPoolWindow after the request
// that took it was admitted. Pools live in this process's memory: a key's
// current settings arrive with each request, so a changed pool applies to the
// next request, and a new worker starts with empty pools.
type RequestPools struct {
	mu      sync.Mutex
	windows map[string]*requestWindow
	sweptAt time.Time
}

func NewRequestPools() *RequestPools {
	return &RequestPools{windows: make(map[string]*requestWindow)}
}

// RequestPoolExhausted refuses a request whose slot would free only after
// the pool's maximum wait.
type RequestPoolExhausted struct {
	Status domain.RequestPoolStatus
}

func (e *RequestPoolExhausted) Error() string { return "API key request pool exhausted" }

// Admit takes one slot of key's pool for a model request. A request over the
// pool waits in arrival order while its slot frees within the pool's maximum
// wait; otherwise, or when its slot would free later, it is refused at once
// with RequestPoolExhausted. A cancelled request leaves the queue and returns
// a slot it was granted at the same time. The wait holds no lock. A key
// without a pool is admitted with a zero status.
func (p *RequestPools) Admit(ctx context.Context, key domain.APIKey) (domain.RequestPoolStatus, error) {
	if key.RequestPool == nil {
		return domain.RequestPoolStatus{}, nil
	}
	return p.admit(ctx, key.ID, *key.RequestPool)
}

// Status reports key's pool without taking a slot.
func (p *RequestPools) Status(key domain.APIKey) domain.RequestPoolStatus {
	if key.RequestPool == nil {
		return domain.RequestPoolStatus{}
	}
	limit := key.RequestPool.RequestsPerMinute
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	w := p.windows[key.ID]
	if w == nil {
		return domain.RequestPoolStatus{Limit: limit, Remaining: limit}
	}
	w.pump(now)
	return w.status(now, limit)
}

func (p *RequestPools) admit(ctx context.Context, id string, pool domain.RequestPool) (domain.RequestPoolStatus, error) {
	if err := ctx.Err(); err != nil {
		return domain.RequestPoolStatus{}, err
	}
	arrival := time.Now()
	deadline := arrival.Add(pool.MaxWait())
	p.mu.Lock()
	p.sweepLocked(arrival)
	w := p.windows[id]
	if w == nil {
		w = &requestWindow{changed: make(chan struct{})}
		p.windows[id] = w
	}
	if w.limit != pool.RequestsPerMinute {
		// Queued requests re-evaluate against the key's current pool.
		w.limit = pool.RequestsPerMinute
		w.wake()
	}
	w.pump(arrival)
	if len(w.waiters) == 0 && len(w.admitted) < w.limit {
		w.admitted = append(w.admitted, arrival)
		status := w.status(arrival, w.limit)
		p.mu.Unlock()
		return status, nil
	}
	free := w.slotFree(arrival, len(w.waiters))
	if free.After(deadline) {
		status := w.status(arrival, w.limit)
		status.RetryAfter = free.Sub(arrival)
		p.mu.Unlock()
		return status, &RequestPoolExhausted{Status: status}
	}
	waiter := &poolWaiter{ready: make(chan struct{})}
	w.waiters = append(w.waiters, waiter)
	changed := w.changed
	p.mu.Unlock()
	timer := time.NewTimer(free.Sub(arrival))
	defer timer.Stop()
	for {
		select {
		case <-waiter.ready:
		case <-ctx.Done():
		case <-timer.C:
		case <-changed:
		}
		p.mu.Lock()
		now := time.Now()
		if err := ctx.Err(); err != nil {
			if waiter.admitted {
				w.release(waiter.at)
			} else {
				w.remove(waiter)
			}
			// Requests behind it move up; a returned slot goes to the next one.
			w.wake()
			w.pump(now)
			p.mu.Unlock()
			return domain.RequestPoolStatus{}, err
		}
		w.pump(now)
		if waiter.admitted {
			status := w.status(now, w.limit)
			p.mu.Unlock()
			return status, nil
		}
		free = w.slotFree(now, w.position(waiter))
		changed = w.changed
		if free.After(deadline) {
			// A lowered pool moved its slot past the maximum wait.
			w.remove(waiter)
			w.wake()
			status := w.status(now, w.limit)
			status.RetryAfter = w.slotFree(now, len(w.waiters)).Sub(now)
			p.mu.Unlock()
			return status, &RequestPoolExhausted{Status: status}
		}
		p.mu.Unlock()
		timer.Reset(free.Sub(now))
	}
}

// sweepLocked drops pools that hold neither slots nor queued requests, at
// most once per window.
func (p *RequestPools) sweepLocked(now time.Time) {
	if now.Sub(p.sweptAt) < domain.RequestPoolWindow {
		return
	}
	p.sweptAt = now
	for id, w := range p.windows {
		w.prune(now)
		if len(w.admitted) == 0 && len(w.waiters) == 0 {
			delete(p.windows, id)
		}
	}
}

// requestWindow is one pool; its owner's mutex guards every field.
type requestWindow struct {
	limit int
	// admitted are the admission times inside the window, ascending.
	admitted []time.Time
	// waiters queue in arrival order.
	waiters []*poolWaiter
	// changed is closed, and replaced, when queued requests may move forward
	// or back without a slot freeing.
	changed chan struct{}
}

type poolWaiter struct {
	ready    chan struct{} // closed on admission
	admitted bool
	at       time.Time
}

func (w *requestWindow) prune(now time.Time) {
	expired := 0
	for expired < len(w.admitted) && !w.admitted[expired].Add(domain.RequestPoolWindow).After(now) {
		expired++
	}
	w.admitted = w.admitted[expired:]
}

// pump admits queued requests in arrival order while slots are free.
func (w *requestWindow) pump(now time.Time) {
	w.prune(now)
	for len(w.waiters) > 0 && len(w.admitted) < w.limit {
		next := w.waiters[0]
		w.waiters[0] = nil
		w.waiters = w.waiters[1:]
		next.admitted, next.at = true, now
		w.admitted = append(w.admitted, now)
		close(next.ready)
	}
}

// slotFree is when the slot of the request queued at position frees, after
// pump: slots free in the order they were taken, the current admissions
// first and then the queued requests ahead.
func (w *requestWindow) slotFree(now time.Time, position int) time.Time {
	index := len(w.admitted) - w.limit + position
	switch {
	case index < 0:
		return now
	case index < len(w.admitted):
		return w.admitted[index].Add(domain.RequestPoolWindow)
	}
	// The slot of the request queued limit places ahead, once it is admitted.
	return w.slotFree(now, index-len(w.admitted)).Add(domain.RequestPoolWindow)
}

func (w *requestWindow) status(now time.Time, limit int) domain.RequestPoolStatus {
	status := domain.RequestPoolStatus{Limit: limit, Remaining: max(limit-len(w.admitted)-len(w.waiters), 0)}
	switch {
	case len(w.waiters) > 0 && w.limit == limit:
		status.Reset = w.slotFree(now, len(w.waiters)-1).Add(domain.RequestPoolWindow).Sub(now)
	case len(w.admitted) > 0:
		status.Reset = w.admitted[len(w.admitted)-1].Add(domain.RequestPoolWindow).Sub(now)
	}
	return status
}

func (w *requestWindow) wake() {
	close(w.changed)
	w.changed = make(chan struct{})
}

func (w *requestWindow) position(waiter *poolWaiter) int {
	return slices.Index(w.waiters, waiter)
}

func (w *requestWindow) remove(waiter *poolWaiter) {
	if i := w.position(waiter); i >= 0 {
		w.waiters = slices.Delete(w.waiters, i, i+1)
	}
}

// release returns the slot taken at at.
func (w *requestWindow) release(at time.Time) {
	for i := len(w.admitted) - 1; i >= 0; i-- {
		if w.admitted[i].Equal(at) {
			w.admitted = slices.Delete(w.admitted, i, i+1)
			return
		}
	}
}
