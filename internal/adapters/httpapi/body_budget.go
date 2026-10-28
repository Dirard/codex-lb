package httpapi

import (
	"errors"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

// bodyChargeChunk bounds how much one read charges ahead of the bytes it
// actually returns.
const bodyChargeChunk = 64 << 10

// A request body must keep arriving: a read that makes no progress for
// bodyIdleTimeout ends the request, and a body is never read for longer than
// RequestBodyTimeout, which the runtime also uses as its server ReadTimeout.
const (
	bodyIdleTimeout    = 10 * time.Second
	RequestBodyTimeout = 2 * time.Minute
)

// BodyBudget bounds the request-body bytes that in-flight proxy requests
// retain, across every proxy route, with an optional per-key share. It counts
// body bytes as they arrive, not process memory, and never blocks: a body that
// does not fit is refused with local capacity.
type BodyBudget struct {
	mu       sync.Mutex
	limit    int64
	keyLimit int64 // keyLimit >= limit disables the per-key share
	used     int64
	keys     map[string]int64
	rejected uint64
	// idleTimeout is bodyIdleTimeout; tests shorten it.
	idleTimeout time.Duration
}

// BodyBudgetUsage reports retained body bytes against the budget.
type BodyBudgetUsage struct {
	UsedBytes     int64
	LimitBytes    int64
	KeyLimitBytes int64
	Rejected      uint64
}

// NewBodyBudget creates a budget of limit bytes of which one key may hold
// keyLimit; a non-positive limit uses the 1 GB-host plan and a non-positive or
// larger keyLimit disables the per-key share.
func NewBodyBudget(limit, keyLimit int64) *BodyBudget {
	if limit <= 0 {
		plan := domain.PlanCapacity(domain.HostResources{}, domain.CapacityOverrides{})
		limit, keyLimit = plan.BodyBudgetBytes, plan.BodyBudgetKeyBytes
	}
	if keyLimit <= 0 || keyLimit > limit {
		keyLimit = limit
	}
	return &BodyBudget{limit: limit, keyLimit: keyLimit, keys: make(map[string]int64), idleTimeout: bodyIdleTimeout}
}

// Usage reports the retained bytes, the bounds and how many bodies were refused.
func (b *BodyBudget) Usage() BodyBudgetUsage {
	b.mu.Lock()
	defer b.mu.Unlock()
	return BodyBudgetUsage{UsedBytes: b.used, LimitBytes: b.limit, KeyLimitBytes: b.keyLimit, Rejected: b.rejected}
}

// lease opens an empty charge for one request body of key.
func (b *BodyBudget) lease(key string) *bodyLease { return &bodyLease{budget: b, key: key} }

// refuse records a refused body and returns the local capacity error.
func (b *BodyBudget) refuse() error {
	b.mu.Lock()
	b.rejected++
	b.mu.Unlock()
	return &application.ProxyError{Code: "local_capacity_exceeded", Status: 503, Message: "Request bodies exceed the local memory budget"}
}

// sharedKey reports whether key is bounded by the per-key share. Keyless local
// traffic is one internal principal for every local client.
func (b *BodyBudget) sharedKey(key string) bool {
	return b.keyLimit < b.limit && key != domain.LocalProxyKeyID
}

// freeLocked is what key may still charge.
func (b *BodyBudget) freeLocked(key string) int64 {
	free := b.limit - b.used
	if b.sharedKey(key) {
		free = min(free, b.keyLimit-b.keys[key])
	}
	return free
}

// bodyLease is one request's charge against the budget. release returns it
// exactly once; later grants and releases are no-ops.
type bodyLease struct {
	budget *BodyBudget
	key    string
	held   int64
	closed bool
}

// grow charges up to n more bytes and returns the bytes granted; with all set
// it grants either n or nothing.
func (l *bodyLease) grow(n int64, all bool) int64 {
	b := l.budget
	b.mu.Lock()
	defer b.mu.Unlock()
	if l.closed || n <= 0 {
		return 0
	}
	granted := min(n, b.freeLocked(l.key))
	if granted <= 0 || all && granted < n {
		return 0
	}
	b.used += granted
	l.held += granted
	if b.sharedKey(l.key) {
		b.keys[l.key] += granted
	}
	return granted
}

// refund returns n granted but unused bytes.
func (l *bodyLease) refund(n int64) {
	b := l.budget
	b.mu.Lock()
	defer b.mu.Unlock()
	n = min(n, l.held)
	if l.closed || n <= 0 {
		return
	}
	l.give(n)
}

// release returns every held byte and closes the lease.
func (l *bodyLease) release() {
	b := l.budget
	b.mu.Lock()
	defer b.mu.Unlock()
	if l.closed {
		return
	}
	l.closed = true
	l.give(l.held)
}

func (l *bodyLease) give(n int64) {
	b := l.budget
	b.used -= n
	l.held -= n
	if b.sharedKey(l.key) {
		if b.keys[l.key] -= n; b.keys[l.key] <= 0 {
			delete(b.keys, l.key)
		}
	}
}

// admit checks a declared body length before any byte is read: a length above
// maximum is too large, one the free budget cannot hold now is refused. Nothing
// is reserved; bytes are charged as they arrive, so a request that declares a
// length and sends nothing holds nothing.
func (l *bodyLease) admit(r *http.Request, maximum int64) error {
	if r.ContentLength > maximum {
		return requestTooLarge()
	}
	if r.ContentLength <= 0 {
		return nil
	}
	b := l.budget
	b.mu.Lock()
	fits := r.ContentLength <= b.freeLocked(l.key)
	b.mu.Unlock()
	if !fits {
		return b.refuse()
	}
	return nil
}

// open admits r's body and returns a reader that charges every byte it returns
// and keeps the read deadline moving while bytes arrive; maximum bounds the
// body as read from the connection.
func (l *bodyLease) open(w http.ResponseWriter, r *http.Request, maximum int64) (io.Reader, error) {
	if err := l.admit(r, maximum); err != nil {
		return nil, err
	}
	var source io.Reader = http.MaxBytesReader(w, r.Body, maximum)
	if r.ContentLength != 0 {
		end := time.Now().Add(RequestBodyTimeout)
		progress := &progressReader{source: source, controller: http.NewResponseController(w), idle: l.budget.idleTimeout, end: end}
		progress.extend()
		source = progress
	}
	return source, nil
}

// chargedReader charges every byte it returns before the caller can retain it,
// granting at most bodyChargeChunk per read and refunding the unread rest. It
// returns at most limit bytes (0: unlimited); one more byte, read without
// charge, means the body is too large rather than over budget.
type chargedReader struct {
	source io.Reader
	lease  *bodyLease
	limit  int64
	read   int64
	err    error
}

func (c *chargedReader) Read(p []byte) (int, error) {
	if c.err != nil {
		return 0, c.err
	}
	if len(p) == 0 {
		return 0, nil
	}
	if c.limit > 0 {
		if c.read >= c.limit {
			c.err = probeEnd(c.source, requestTooLarge)
			var tooLarge *http.MaxBytesError
			if errors.As(c.err, &tooLarge) {
				c.err = requestTooLarge()
			}
			return 0, c.err
		}
		p = p[:min(int64(len(p)), c.limit-c.read)]
	}
	granted := c.lease.grow(min(int64(len(p)), bodyChargeChunk), false)
	if granted == 0 {
		// Nothing is free: the body fits only if it has already ended.
		c.err = probeEnd(c.source, c.lease.budget.refuse)
		return 0, c.err
	}
	n, err := c.source.Read(p[:granted])
	c.read += int64(n)
	c.lease.refund(granted - int64(n))
	return n, err
}

// probeEnd reads one byte without charging it: the end of the body yields
// io.EOF, a further byte the failure.
func probeEnd(source io.Reader, failure func() error) error {
	var probe [1]byte
	if _, err := io.ReadAtLeast(source, probe[:], 1); err != nil {
		return err
	}
	return failure()
}

// progressReader moves the connection's read deadline to idle after every read
// that returns data, never past end, so a stalled upload ends the request and
// releases what it was charged. Once the body has ended the deadline is left
// alone: the server clears it for its own background read.
type progressReader struct {
	source     io.Reader
	controller *http.ResponseController
	idle       time.Duration
	end        time.Time
}

func (p *progressReader) extend() {
	deadline := time.Now().Add(p.idle)
	if deadline.After(p.end) {
		deadline = p.end
	}
	// Unsupported writers (tests' recorders) keep the server's ReadTimeout.
	_ = p.controller.SetReadDeadline(deadline)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.source.Read(b)
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return n, &application.ProxyError{Code: "request_body_timeout", Status: 408, Message: "Request body stopped arriving"}
	}
	if n > 0 && err == nil {
		p.extend()
	}
	return n, err
}

// readChargedBody reads a whole body through its charging reader.
func readChargedBody(reader *chargedReader) ([]byte, error) {
	body, err := io.ReadAll(reader)
	if failure := bodyFailure(err); failure != nil {
		return nil, failure
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return nil, requestTooLarge()
	}
	if err != nil {
		return nil, &application.ProxyError{Code: "invalid_request_body", Status: 400, Message: "Could not decode request body"}
	}
	return body, nil
}

// bodyFailure returns the local error (budget refusal, size or timeout) a body
// read carried, if any.
func bodyFailure(err error) error {
	var failure *application.ProxyError
	if errors.As(err, &failure) {
		return failure
	}
	return nil
}

func requestTooLarge() error {
	return &application.ProxyError{Code: "request_too_large", Status: 413, Message: "Request body exceeds size limit"}
}
