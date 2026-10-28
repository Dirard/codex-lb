package httpapi

import (
	"errors"
	"io"
	"net/http"
	"sync"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

// bodyChargeChunk bounds how much one read of an undeclared body charges ahead
// of the bytes it actually returns.
const bodyChargeChunk = 64 << 10

// BodyBudget bounds the request-body bytes that in-flight proxy requests
// retain, across every proxy route, with an optional per-key share. It counts
// body bytes, not process memory, and never blocks: a body that does not fit
// is refused with local capacity.
type BodyBudget struct {
	mu       sync.Mutex
	limit    int64
	keyLimit int64 // keyLimit >= limit disables the per-key share
	used     int64
	keys     map[string]int64
	rejected uint64
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
	return &BodyBudget{limit: limit, keyLimit: keyLimit, keys: make(map[string]int64)}
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
	free := b.limit - b.used
	if b.sharedKey(l.key) {
		free = min(free, b.keyLimit-b.keys[l.key])
	}
	granted := min(n, free)
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

// chargedReader charges every byte it returns before the caller can retain it.
// prepaid bytes (a declared Content-Length) were charged up front; beyond them
// each read is granted at most bodyChargeChunk and the unread rest refunded.
type chargedReader struct {
	source  io.Reader
	lease   *bodyLease
	prepaid int64
}

func (c *chargedReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if c.prepaid > 0 {
		n, err := c.source.Read(p[:min(int64(len(p)), c.prepaid)])
		c.prepaid -= int64(n)
		return n, err
	}
	granted := c.lease.grow(min(int64(len(p)), bodyChargeChunk), false)
	if granted == 0 {
		// Nothing is free: the body fits only if it has already ended.
		var probe [1]byte
		if _, err := io.ReadAtLeast(c.source, probe[:], 1); err != nil {
			return 0, err
		}
		return 0, c.lease.budget.refuse()
	}
	n, err := c.source.Read(p[:granted])
	c.lease.refund(granted - int64(n))
	return n, err
}

// admitDeclared charges a declared body length before any byte is read: a
// length above maximum is too large, one the budget cannot hold is refused. It
// returns the prepaid length, 0 when the length is undeclared.
func (l *bodyLease) admitDeclared(r *http.Request, maximum int64) (int64, error) {
	if r.ContentLength > maximum {
		return 0, requestTooLarge()
	}
	if r.ContentLength <= 0 {
		return 0, nil
	}
	if l.grow(r.ContentLength, true) == 0 {
		return 0, l.budget.refuse()
	}
	return r.ContentLength, nil
}

// readDeclaredBody reads a body of known, already charged length into one
// exactly sized buffer.
func readDeclaredBody(source io.Reader, length int64) ([]byte, error) {
	body := make([]byte, length)
	if _, err := io.ReadFull(source, body); err != nil {
		return nil, &application.ProxyError{Code: "invalid_request_body", Status: 400, Message: "Could not decode request body"}
	}
	return body, nil
}

// readChargedBody reads a body of unknown length through its charging reader.
func readChargedBody(reader *chargedReader, maximum int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, maximum+1))
	if refused := budgetRefusal(err); refused != nil {
		return nil, refused
	}
	var tooLarge *http.MaxBytesError
	if int64(len(body)) > maximum || errors.As(err, &tooLarge) {
		return nil, requestTooLarge()
	}
	if err != nil {
		return nil, &application.ProxyError{Code: "invalid_request_body", Status: 400, Message: "Could not decode request body"}
	}
	return body, nil
}

// budgetRefusal returns the budget's local capacity error carried by err.
func budgetRefusal(err error) error {
	var refused *application.ProxyError
	if errors.As(err, &refused) && refused.Code == "local_capacity_exceeded" {
		return refused
	}
	return nil
}

func requestTooLarge() error {
	return &application.ProxyError{Code: "request_too_large", Status: 413, Message: "Request body exceeds size limit"}
}
