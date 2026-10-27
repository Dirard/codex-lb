package application

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestRunPeriodicRecoversPanicsAndReportsEveryRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var reports []error
	runs := 0
	trigger := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		RunPeriodic(ctx, "test", BackgroundSchedule{Report: func(task string, _ time.Duration, err error) {
			if task != "test" {
				t.Errorf("task = %q", task)
			}
			mu.Lock()
			reports = append(reports, err)
			mu.Unlock()
		}}, time.Hour, trigger, func(context.Context) error {
			runs++
			switch runs {
			case 1:
				panic("boom")
			case 2:
				return errors.New("failed")
			}
			return nil
		})
	}()
	waitReports := func(n int) {
		t.Helper()
		deadline := time.After(2 * time.Second)
		for {
			mu.Lock()
			got := len(reports)
			mu.Unlock()
			if got >= n {
				return
			}
			select {
			case <-deadline:
				t.Fatalf("only %d reports", got)
			case <-time.After(time.Millisecond):
			}
		}
	}
	waitReports(1)
	trigger <- struct{}{}
	waitReports(2)
	trigger <- struct{}{}
	waitReports(3)
	cancel()
	<-done
	var panicked *PanicError
	if !errors.As(reports[0], &panicked) || panicked.Value != "boom" || len(panicked.Stack) == 0 {
		t.Fatalf("first report = %v", reports[0])
	}
	if reports[1] == nil || reports[1].Error() != "failed" || reports[2] != nil {
		t.Fatalf("reports = %v", reports)
	}
}

func TestRunPeriodicHonorsInitialDelayAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		RunPeriodic(ctx, "delayed", BackgroundSchedule{InitialDelay: time.Hour}, time.Hour, nil, func(context.Context) error {
			ran <- struct{}{}
			return nil
		})
	}()
	select {
	case <-ran:
		t.Fatal("task ran before its initial delay")
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("loop did not stop on cancellation")
	}
}
