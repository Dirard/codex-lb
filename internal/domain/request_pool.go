package domain

import (
	"fmt"
	"time"
)

// RequestPool caps how many model requests an API key starts per minute. A
// request over the pool waits up to MaxWaitSeconds for a slot to free. It is a
// request-rate gate in front of the proxy, not a budget limit of the ledger.
type RequestPool struct {
	RequestsPerMinute int `json:"requestsPerMinute"`
	MaxWaitSeconds    int `json:"maxWaitSeconds"`
}

const (
	// RequestPoolWindow is the interval a pool counts admissions over: a slot
	// frees one window after the request that took it was admitted.
	RequestPoolWindow = time.Minute
	// MaxRequestPoolRequestsPerMinute bounds a pool, and with it the memory
	// and queue of one key.
	MaxRequestPoolRequestsPerMinute = 10000
	// MaxRequestPoolWaitSeconds keeps the maximum wait within one window.
	MaxRequestPoolWaitSeconds = 60
	// DefaultRequestPoolWaitSeconds applies when a pool omits its maximum wait.
	DefaultRequestPoolWaitSeconds = 30
)

func (p RequestPool) Validate() error {
	if p.RequestsPerMinute < 1 || p.RequestsPerMinute > MaxRequestPoolRequestsPerMinute {
		return fmt.Errorf("request pool requests per minute must be 1-%d: %w", MaxRequestPoolRequestsPerMinute, ErrInvalid)
	}
	if p.MaxWaitSeconds < 0 || p.MaxWaitSeconds > MaxRequestPoolWaitSeconds {
		return fmt.Errorf("request pool maximum wait must be 0-%d seconds: %w", MaxRequestPoolWaitSeconds, ErrInvalid)
	}
	return nil
}

// MaxWait is how long a request over the pool may wait for a slot.
func (p RequestPool) MaxWait() time.Duration { return time.Duration(p.MaxWaitSeconds) * time.Second }

// RequestPoolStatus is a pool as one request sees it after its admission or
// refusal. Limit is zero when the key has no pool.
type RequestPoolStatus struct {
	Limit int
	// Remaining are the slots free now, after this request; zero while
	// requests queue.
	Remaining int
	// Reset is the time until every slot taken now is free again.
	Reset time.Duration
	// RetryAfter, on a refusal, is the time until the slot a new request would
	// get frees.
	RetryAfter time.Duration
	// Fork marks a fork's request. ForkUsed counts the fork's slots in the
	// pool and ForkLimit is its own cap, zero when it has none.
	Fork      bool
	ForkUsed  int
	ForkLimit int
}

// Pooled reports whether the status describes a pool.
func (s RequestPoolStatus) Pooled() bool { return s.Limit > 0 }

// RetryAfterSeconds rounds RetryAfter up to whole seconds, at least one.
func (s RequestPoolStatus) RetryAfterSeconds() int64 {
	seconds := int64((s.RetryAfter + time.Second - 1) / time.Second)
	return max(seconds, 1)
}
