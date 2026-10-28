package application

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"
)

// BackgroundReport receives the outcome of every run of a background task;
// err is nil on success and a *PanicError when the run panicked.
type BackgroundReport func(task string, duration time.Duration, err error)

// BackgroundSchedule configures how a service's periodic loop starts and
// reports. The zero value runs immediately and reports nothing.
type BackgroundSchedule struct {
	InitialDelay time.Duration
	Report       BackgroundReport
}

// PanicError carries a recovered panic and its stack out of a background run.
type PanicError struct {
	Value any
	Stack []byte
}

func (e *PanicError) Error() string { return fmt.Sprintf("background task panicked: %T", e.Value) }

// RunPeriodic runs fn after the schedule's initial delay, then every interval
// and whenever trigger fires, until ctx ends. A panic inside fn is recovered
// and reported so one failing run does not stop the service's other work.
func RunPeriodic(ctx context.Context, task string, schedule BackgroundSchedule, interval time.Duration, trigger <-chan struct{}, fn func(context.Context) error) {
	if schedule.InitialDelay > 0 {
		timer := time.NewTimer(schedule.InitialDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		case <-trigger:
			timer.Stop()
		}
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		started := time.Now()
		err := runRecovered(ctx, fn)
		if schedule.Report != nil && ctx.Err() == nil {
			schedule.Report(task, time.Since(started), err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-trigger:
		}
	}
}

func runRecovered(ctx context.Context, fn func(context.Context) error) (err error) {
	defer func() {
		if value := recover(); value != nil {
			err = &PanicError{Value: value, Stack: debug.Stack()}
		}
	}()
	return fn(ctx)
}
