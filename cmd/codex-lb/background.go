package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"codex-lb/internal/adapters/metrics"
	"codex-lb/internal/application"
)

// repeatedFailureLogInterval bounds how often an unchanged failure is logged.
const repeatedFailureLogInterval = 10 * time.Minute

// backgroundReporter logs background-task outcomes without flooding: a new
// failure class is logged immediately, a persisting one every ten minutes,
// and recovery once. Panics always log their stack.
type backgroundReporter struct {
	logger   *slog.Logger
	registry *metrics.Registry
	now      func() time.Time
	mu       sync.Mutex
	state    map[string]failureState
}

type failureState struct {
	class    string
	since    time.Time
	lastLog  time.Time
	failures int
}

func newBackgroundReporter(logger *slog.Logger, registry *metrics.Registry) *backgroundReporter {
	return &backgroundReporter{logger: logger, registry: registry, now: time.Now, state: map[string]failureState{}}
}

func (b *backgroundReporter) report(task string, elapsed time.Duration, err error) {
	now := b.now()
	var panicked *application.PanicError
	isPanic := errors.As(err, &panicked)
	if b.registry != nil {
		b.registry.TaskFinished(task, elapsed, err != nil && !isPanic, isPanic, now)
	}
	if isPanic {
		b.logger.Error("background task panicked", "task", task, "panic_type", fmt.Sprintf("%T", panicked.Value), "stack", string(panicked.Stack))
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	previous, failing := b.state[task]
	if err == nil {
		if failing {
			b.logger.Info("background task recovered", "task", task, "failed_runs", previous.failures, "failing_for_s", int(now.Sub(previous.since).Seconds()))
			delete(b.state, task)
		}
		return
	}
	class := errorClass(err)
	current := failureState{class: class, since: now, lastLog: previous.lastLog, failures: previous.failures + 1}
	if failing && previous.class == class {
		current.since = previous.since
	}
	if !isPanic && (!failing || previous.class != class || now.Sub(previous.lastLog) >= repeatedFailureLogInterval) {
		b.logger.Warn("background task failed", "task", task, "error_class", class, "failed_runs", current.failures, "duration_ms", elapsed.Milliseconds())
		current.lastLog = now
	}
	b.state[task] = current
}

// errorClass names an error without its message: provider and database
// errors can carry request data, so only stable codes and types are logged.
func errorClass(err error) string {
	var proxyErr *application.ProxyError
	var providerErr *application.ProviderFailure
	var panicked *application.PanicError
	switch {
	case errors.As(err, &panicked):
		return "panic"
	case errors.As(err, &proxyErr):
		return "proxy:" + proxyErr.Code
	case errors.As(err, &providerErr):
		return "provider:" + providerErr.Code
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	}
	name := fmt.Sprintf("%T", err)
	return strings.TrimPrefix(name, "*")
}

// startupJitter spreads the first background run so restart loops do not
// burst every account's usage poll or the catalog at once.
func startupJitter(minimum, spread time.Duration) time.Duration {
	if spread <= 0 {
		return minimum
	}
	return minimum + rand.N(spread)
}
