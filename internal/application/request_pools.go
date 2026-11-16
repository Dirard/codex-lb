package application

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"codex-lb/internal/domain"
)

// RequestPools admits the model requests of API keys that have a request
// pool. Each pool is a sliding-window log of admission times with a queue in
// arrival order; a slot frees one domain.RequestPoolWindow after the request
// that took it was admitted. A parent's pool is shared by the parent and its
// forks, and a fork may have its own cap inside it. Pools live in this
// process's memory: a key's current settings arrive with each request, so a
// changed pool applies to the next request, and a new worker starts with
// empty pools.
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

// poolPlan is where a key's requests are counted: its own pool, or its
// parent's shared pool and, for a capped fork, its cap inside it.
type poolPlan struct {
	poolID string
	pool   domain.RequestPool
	capID  string
	cap    *domain.RequestPool
	fork   bool
	keyID  string
}

// capOwner is the key whose own pool this is.
func (plan poolPlan) capOwner() string { return plan.keyID }

// forkShare reports a fork's slots in its pool and, when the fork's own pool
// is its cap, that cap.
func (plan poolPlan) forkShare(status *windowStatus) {
	if !plan.fork {
		return
	}
	status.Fork, status.ForkUsed = true, status.memberUsed
	if plan.poolID != plan.capOwner() {
		return
	}
	status.ForkLimit = status.Limit
}

func planFor(key domain.APIKey) (poolPlan, bool) {
	fork := key.Fork != nil
	if fork && key.Fork.ParentPool != nil {
		plan := poolPlan{poolID: key.Fork.ParentID, pool: *key.Fork.ParentPool, fork: true, keyID: key.ID}
		if key.RequestPool != nil {
			// A cap's window never shares an id with a key's own pool.
			plan.capID, plan.cap = "cap:"+key.ID, key.RequestPool
		}
		return plan, true
	}
	if key.RequestPool != nil {
		return poolPlan{poolID: key.ID, pool: *key.RequestPool, fork: fork, keyID: key.ID}, true
	}
	return poolPlan{}, false
}

// Admit takes one slot of key's pool for a model request. A request over the
// pool waits in arrival order while its slot frees within the pool's maximum
// wait; otherwise, or when its slot would free later, it is refused at once
// with RequestPoolExhausted. A capped fork takes a slot under its cap and
// then a shared slot, both before the earlier maximum wait, and returns the
// cap slot when it gets no shared slot. A cancelled request leaves the queue
// and returns a slot it was granted at the same time. The wait holds no
// lock. A key without a pool is admitted with a zero status.
func (p *RequestPools) Admit(ctx context.Context, key domain.APIKey) (domain.RequestPoolStatus, error) {
	plan, ok := planFor(key)
	if !ok {
		return domain.RequestPoolStatus{}, nil
	}
	if err := ctx.Err(); err != nil {
		return domain.RequestPoolStatus{}, err
	}
	arrival := time.Now()
	if plan.cap == nil {
		status, _, err := p.admit(ctx, plan.poolID, key.ID, plan.pool.RequestsPerMinute, arrival, arrival.Add(plan.pool.MaxWait()))
		plan.forkShare(&status)
		return refusalStatus(status.RequestPoolStatus, err)
	}
	deadline := arrival.Add(min(plan.pool.MaxWait(), plan.cap.MaxWait()))
	capped, capAt, err := p.admit(ctx, plan.capID, key.ID, plan.cap.RequestsPerMinute, arrival, deadline)
	if err != nil {
		status := p.peek(plan.poolID, key.ID, plan.pool.RequestsPerMinute, time.Now())
		status.Remaining, status.RetryAfter = 0, capped.RetryAfter
		status.Fork, status.ForkUsed, status.ForkLimit = true, status.memberUsed, plan.cap.RequestsPerMinute
		return refusalStatus(status.RequestPoolStatus, err)
	}
	// The shared stage starts now, after any wait for the cap, by the same deadline.
	shared, sharedAt, err := p.admit(ctx, plan.poolID, key.ID, plan.pool.RequestsPerMinute, time.Now(), deadline)
	p.mu.Lock()
	if w := p.windows[plan.capID]; w != nil {
		if err != nil {
			w.release(capAt, key.ID)
			w.wake()
			w.pump(time.Now())
		} else {
			// The cap counts from the shared admission.
			w.restamp(capAt, sharedAt, key.ID)
			capped = w.status(time.Now(), w.limit, key.ID)
		}
	}
	p.mu.Unlock()
	shared.Fork, shared.ForkUsed, shared.ForkLimit = true, shared.memberUsed, plan.cap.RequestsPerMinute
	if err == nil {
		shared.Remaining = min(shared.Remaining, capped.Remaining)
	}
	return refusalStatus(shared.RequestPoolStatus, err)
}

// refusalStatus carries status into a refusal.
func refusalStatus(status domain.RequestPoolStatus, err error) (domain.RequestPoolStatus, error) {
	var exhausted *RequestPoolExhausted
	if errors.As(err, &exhausted) {
		return status, &RequestPoolExhausted{Status: status}
	}
	return status, err
}

// Status reports key's pool without taking a slot.
func (p *RequestPools) Status(key domain.APIKey) domain.RequestPoolStatus {
	plan, ok := planFor(key)
	if !ok {
		return domain.RequestPoolStatus{}
	}
	now := time.Now()
	status := p.peek(plan.poolID, key.ID, plan.pool.RequestsPerMinute, now)
	plan.forkShare(&status)
	if plan.cap != nil {
		capped := p.peek(plan.capID, key.ID, plan.cap.RequestsPerMinute, now)
		status.Remaining, status.ForkLimit = min(status.Remaining, capped.Remaining), plan.cap.RequestsPerMinute
	}
	return status.RequestPoolStatus
}

func (p *RequestPools) peek(id, member string, limit int, now time.Time) windowStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	w := p.windows[id]
	if w == nil {
		return windowStatus{RequestPoolStatus: domain.RequestPoolStatus{Limit: limit, Remaining: limit}}
	}
	w.pump(now)
	return w.status(now, limit, member)
}

// admit takes a slot of window id with limit for member, waiting from now
// until deadline at most, and reports the admission time.
func (p *RequestPools) admit(ctx context.Context, id, member string, limit int, arrival, deadline time.Time) (windowStatus, time.Time, error) {
	p.mu.Lock()
	p.sweepLocked(arrival)
	w := p.windows[id]
	if w == nil {
		w = &requestWindow{changed: make(chan struct{}), members: make(map[string]int)}
		p.windows[id] = w
	}
	if w.limit != limit {
		// Queued requests re-evaluate against the key's current pool.
		w.limit = limit
		w.wake()
	}
	w.pump(arrival)
	if len(w.waiters) == 0 && len(w.admitted) < w.limit {
		w.take(arrival, member)
		status := w.status(arrival, w.limit, member)
		p.mu.Unlock()
		return status, arrival, nil
	}
	free := w.slotFree(arrival, len(w.waiters))
	if free.After(deadline) {
		status := w.status(arrival, w.limit, member)
		status.RetryAfter = free.Sub(arrival)
		p.mu.Unlock()
		return status, time.Time{}, &RequestPoolExhausted{Status: status.RequestPoolStatus}
	}
	waiter := &poolWaiter{ready: make(chan struct{}), member: member}
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
				w.release(waiter.at, member)
			} else {
				w.remove(waiter)
			}
			// Requests behind it move up; a returned slot goes to the next one.
			w.wake()
			w.pump(now)
			p.mu.Unlock()
			return windowStatus{}, time.Time{}, err
		}
		w.pump(now)
		if waiter.admitted {
			status := w.status(now, w.limit, member)
			p.mu.Unlock()
			return status, waiter.at, nil
		}
		free = w.slotFree(now, w.position(waiter))
		changed = w.changed
		if free.After(deadline) {
			// A lowered pool moved its slot past the maximum wait.
			w.remove(waiter)
			w.wake()
			status := w.status(now, w.limit, member)
			status.RetryAfter = w.slotFree(now, len(w.waiters)).Sub(now)
			p.mu.Unlock()
			return status, time.Time{}, &RequestPoolExhausted{Status: status.RequestPoolStatus}
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

// windowStatus is a window's status for one key, with that key's own slots.
type windowStatus struct {
	domain.RequestPoolStatus
	memberUsed int
}

// requestWindow is one pool; its owner's mutex guards every field.
type requestWindow struct {
	limit int
	// admitted are the slots taken inside the window, ascending by time.
	admitted []poolSlot
	// members counts the admitted slots of each key.
	members map[string]int
	// waiters queue in arrival order.
	waiters []*poolWaiter
	// changed is closed, and replaced, when queued requests may move forward
	// or back without a slot freeing.
	changed chan struct{}
}

type poolSlot struct {
	at     time.Time
	member string
}

type poolWaiter struct {
	ready    chan struct{} // closed on admission
	member   string
	admitted bool
	at       time.Time
}

func (w *requestWindow) prune(now time.Time) {
	expired := 0
	for expired < len(w.admitted) && !w.admitted[expired].at.Add(domain.RequestPoolWindow).After(now) {
		w.forget(w.admitted[expired].member)
		expired++
	}
	w.admitted = w.admitted[expired:]
}

func (w *requestWindow) take(at time.Time, member string) {
	w.admitted = append(w.admitted, poolSlot{at, member})
	w.members[member]++
}

func (w *requestWindow) forget(member string) {
	if w.members[member]--; w.members[member] <= 0 {
		delete(w.members, member)
	}
}

// pump admits queued requests in arrival order while slots are free.
func (w *requestWindow) pump(now time.Time) {
	w.prune(now)
	for len(w.waiters) > 0 && len(w.admitted) < w.limit {
		next := w.waiters[0]
		w.waiters[0] = nil
		w.waiters = w.waiters[1:]
		next.admitted, next.at = true, now
		w.take(now, next.member)
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
		return w.admitted[index].at.Add(domain.RequestPoolWindow)
	}
	// The slot of the request queued limit places ahead, once it is admitted.
	return w.slotFree(now, index-len(w.admitted)).Add(domain.RequestPoolWindow)
}

func (w *requestWindow) status(now time.Time, limit int, member string) windowStatus {
	status := windowStatus{domain.RequestPoolStatus{Limit: limit, Remaining: max(limit-len(w.admitted)-len(w.waiters), 0)}, w.members[member]}
	switch {
	case len(w.waiters) > 0 && w.limit == limit:
		status.Reset = w.slotFree(now, len(w.waiters)-1).Add(domain.RequestPoolWindow).Sub(now)
	case len(w.admitted) > 0:
		status.Reset = w.admitted[len(w.admitted)-1].at.Add(domain.RequestPoolWindow).Sub(now)
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

// release returns member's slot taken at at.
func (w *requestWindow) release(at time.Time, member string) bool {
	for i := len(w.admitted) - 1; i >= 0; i-- {
		if slot := w.admitted[i]; slot.member == member && slot.at.Equal(at) {
			w.admitted = slices.Delete(w.admitted, i, i+1)
			w.forget(member)
			return true
		}
	}
	return false
}

// restamp moves member's slot taken at from to at, keeping the order.
func (w *requestWindow) restamp(from, at time.Time, member string) {
	if from.Equal(at) || !w.release(from, member) {
		return
	}
	i, _ := slices.BinarySearchFunc(w.admitted, at, func(slot poolSlot, at time.Time) int { return slot.at.Compare(at) })
	for i < len(w.admitted) && !w.admitted[i].at.After(at) {
		i++
	}
	w.admitted = slices.Insert(w.admitted, i, poolSlot{at, member})
	w.members[member]++
}
