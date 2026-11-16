package application

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"codex-lb/internal/domain"
)

func pooledKey(id string, rpm, wait int) domain.APIKey {
	return domain.APIKey{ID: id, RequestPool: &domain.RequestPool{RequestsPerMinute: rpm, MaxWaitSeconds: wait}}
}

type poolOutcome struct {
	status domain.RequestPoolStatus
	err    error
	at     time.Time
}

func admitAsync(ctx context.Context, pools *RequestPools, key domain.APIKey) <-chan poolOutcome {
	done := make(chan poolOutcome, 1)
	go func() {
		status, err := pools.Admit(ctx, key)
		done <- poolOutcome{status, err, time.Now()}
	}()
	return done
}

func poolPending(t *testing.T, name string, done <-chan poolOutcome) {
	t.Helper()
	synctest.Wait()
	select {
	case outcome := <-done:
		t.Fatalf("%s finished early: %+v", name, outcome)
	default:
	}
}

func poolFinished(t *testing.T, name string, done <-chan poolOutcome) poolOutcome {
	t.Helper()
	synctest.Wait()
	select {
	case outcome := <-done:
		return outcome
	default:
		t.Fatalf("%s still waiting", name)
		return poolOutcome{}
	}
}

func mustAdmit(t *testing.T, pools *RequestPools, key domain.APIKey) domain.RequestPoolStatus {
	t.Helper()
	status, err := pools.Admit(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func refusal(t *testing.T, err error) domain.RequestPoolStatus {
	t.Helper()
	var exhausted *RequestPoolExhausted
	if !errors.As(err, &exhausted) {
		t.Fatalf("want a pool refusal, got %v", err)
	}
	return exhausted.Status
}

func TestRequestPoolWaitsForTheOldestSlot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pools := NewRequestPools()
		key := pooledKey("k", 2, 30)
		start := time.Now()
		if status := mustAdmit(t, pools, key); status != (domain.RequestPoolStatus{Limit: 2, Remaining: 1, Reset: time.Minute}) {
			t.Fatalf("first %+v", status)
		}
		time.Sleep(10 * time.Second)
		if status := mustAdmit(t, pools, key); status != (domain.RequestPoolStatus{Limit: 2, Remaining: 0, Reset: time.Minute}) {
			t.Fatalf("second %+v", status)
		}
		time.Sleep(30 * time.Second)
		third := admitAsync(context.Background(), pools, key)
		poolPending(t, "third", third)
		if status := pools.Status(key); status.Remaining != 0 || status.Reset != 80*time.Second {
			t.Fatalf("queued status %+v", status)
		}
		time.Sleep(20 * time.Second)
		outcome := poolFinished(t, "third", third)
		if outcome.err != nil || !outcome.at.Equal(start.Add(time.Minute)) ||
			outcome.status != (domain.RequestPoolStatus{Limit: 2, Remaining: 0, Reset: time.Minute}) {
			t.Fatalf("third %+v", outcome)
		}
	})
}

func TestRequestPoolRefusesWhenTheSlotFreesAfterTheMaximumWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pools := NewRequestPools()
		key := pooledKey("k", 1, 10)
		mustAdmit(t, pools, key)
		_, err := pools.Admit(context.Background(), key)
		status := refusal(t, err)
		if status.Limit != 1 || status.Remaining != 0 || status.RetryAfter != time.Minute || status.RetryAfterSeconds() != 60 || status.Reset != time.Minute {
			t.Fatalf("refusal %+v", status)
		}
		time.Sleep(55 * time.Second)
		late := admitAsync(context.Background(), pools, key)
		poolPending(t, "late", late)
		time.Sleep(5 * time.Second)
		if outcome := poolFinished(t, "late", late); outcome.err != nil {
			t.Fatal(outcome.err)
		}
	})
}

func TestRequestPoolWithoutWaitRefusesUntilTheSlotFrees(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pools := NewRequestPools()
		key := pooledKey("k", 1, 0)
		mustAdmit(t, pools, key)
		time.Sleep(59*time.Second + 500*time.Millisecond)
		_, err := pools.Admit(context.Background(), key)
		if status := refusal(t, err); status.RetryAfter != 500*time.Millisecond || status.RetryAfterSeconds() != 1 {
			t.Fatalf("refusal %+v", status)
		}
		time.Sleep(500 * time.Millisecond)
		if status := mustAdmit(t, pools, key); status.Remaining != 0 {
			t.Fatalf("status %+v", status)
		}
	})
}

func TestRequestPoolQueuesInArrivalOrderAndBoundsTheQueue(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pools := NewRequestPools()
		key := pooledKey("k", 2, 60)
		start := time.Now()
		mustAdmit(t, pools, key)
		time.Sleep(10 * time.Second)
		mustAdmit(t, pools, key)
		time.Sleep(10 * time.Second)
		first := admitAsync(context.Background(), pools, key)
		synctest.Wait()
		time.Sleep(time.Second)
		second := admitAsync(context.Background(), pools, key)
		synctest.Wait()
		// Two slots are taken and two requests queue: a fifth would get the
		// slot of the first queued request, a minute after its admission.
		_, err := pools.Admit(context.Background(), key)
		if status := refusal(t, err); status.RetryAfter != 99*time.Second || status.Remaining != 0 {
			t.Fatalf("refusal %+v", status)
		}
		time.Sleep(39 * time.Second)
		if outcome := poolFinished(t, "first", first); outcome.err != nil || !outcome.at.Equal(start.Add(time.Minute)) {
			t.Fatalf("first %+v", outcome)
		}
		poolPending(t, "second", second)
		time.Sleep(10 * time.Second)
		if outcome := poolFinished(t, "second", second); outcome.err != nil || !outcome.at.Equal(start.Add(70*time.Second)) {
			t.Fatalf("second %+v", outcome)
		}
	})
}

func TestRequestPoolCancelledWaiterTakesNoSlot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pools := NewRequestPools()
		key := pooledKey("k", 2, 60)
		start := time.Now()
		mustAdmit(t, pools, key)
		time.Sleep(10 * time.Second)
		mustAdmit(t, pools, key)
		time.Sleep(10 * time.Second)
		ctx, cancel := context.WithCancel(context.Background())
		cancelled := admitAsync(ctx, pools, key)
		synctest.Wait()
		time.Sleep(time.Second)
		next := admitAsync(context.Background(), pools, key)
		time.Sleep(9 * time.Second)
		cancel()
		if outcome := poolFinished(t, "cancelled", cancelled); !errors.Is(outcome.err, context.Canceled) {
			t.Fatalf("cancelled %+v", outcome)
		}
		// The request behind moves up to the slot that frees first.
		time.Sleep(30 * time.Second)
		if outcome := poolFinished(t, "next", next); outcome.err != nil || !outcome.at.Equal(start.Add(time.Minute)) {
			t.Fatalf("next %+v", outcome)
		}
		if status := pools.Status(key); status.Remaining != 0 || status.Reset != 60*time.Second {
			t.Fatalf("status %+v", status)
		}
	})
}

// A request cancelled at the instant another request's admission granted it
// a slot gives the slot back.
func TestRequestPoolReturnsASlotGrantedAtCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pools := NewRequestPools()
		key := pooledKey("k", 1, 60)
		mustAdmit(t, pools, key)
		ctx, cancel := context.WithCancel(context.Background())
		waiting := admitAsync(ctx, pools, key)
		poolPending(t, "waiting", waiting)
		// Holding the pools' lock orders the cancellation before the waiter
		// sees its grant.
		pools.mu.Lock()
		cancel()
		window := pools.windows["k"]
		window.limit = 2
		window.pump(time.Now())
		if len(window.admitted) != 2 {
			pools.mu.Unlock()
			t.Fatal("waiter not granted")
		}
		pools.mu.Unlock()
		if outcome := poolFinished(t, "waiting", waiting); !errors.Is(outcome.err, context.Canceled) {
			t.Fatalf("waiting %+v", outcome)
		}
		if status := pools.Status(pooledKey("k", 2, 60)); status.Remaining != 1 {
			t.Fatalf("slot kept by a cancelled request: %+v", status)
		}
	})
}

func TestRequestPoolAppliesAChangedLimitToQueuedRequests(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pools := NewRequestPools()
		start := time.Now()
		mustAdmit(t, pools, pooledKey("k", 1, 60))
		queued := admitAsync(context.Background(), pools, pooledKey("k", 1, 60))
		poolPending(t, "queued", queued)
		time.Sleep(5 * time.Second)
		// A raised pool admits the queued request at once; the newcomer waits
		// behind it for the first slot to free.
		newcomer := admitAsync(context.Background(), pools, pooledKey("k", 2, 60))
		if outcome := poolFinished(t, "queued", queued); outcome.err != nil || !outcome.at.Equal(start.Add(5*time.Second)) {
			t.Fatalf("queued %+v", outcome)
		}
		poolPending(t, "newcomer", newcomer)
		time.Sleep(55 * time.Second)
		if outcome := poolFinished(t, "newcomer", newcomer); outcome.err != nil || outcome.status.Limit != 2 {
			t.Fatalf("newcomer %+v", outcome)
		}

		// A lowered pool delays a queued request to the next slot that fits.
		mustAdmit(t, pools, pooledKey("l", 2, 60))
		time.Sleep(10 * time.Second)
		mustAdmit(t, pools, pooledKey("l", 2, 60))
		time.Sleep(10 * time.Second)
		lowered := admitAsync(context.Background(), pools, pooledKey("l", 2, 60))
		synctest.Wait()
		time.Sleep(10 * time.Second)
		if _, err := pools.Admit(context.Background(), pooledKey("l", 1, 0)); err == nil {
			t.Fatal("lowered pool admitted a request")
		}
		time.Sleep(30 * time.Second)
		poolPending(t, "lowered", lowered)
		time.Sleep(10 * time.Second)
		if outcome := poolFinished(t, "lowered", lowered); outcome.err != nil || outcome.status.Limit != 1 {
			t.Fatalf("lowered %+v", outcome)
		}
	})
}

func TestRequestPoolWithoutPoolAndStatus(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pools := NewRequestPools()
		if status, err := pools.Admit(context.Background(), domain.APIKey{ID: "free"}); err != nil || status.Pooled() {
			t.Fatalf("unpooled %+v %v", status, err)
		}
		if len(pools.windows) != 0 {
			t.Fatal("unpooled key created a pool")
		}
		key := pooledKey("k", 3, 30)
		if status := pools.Status(key); status != (domain.RequestPoolStatus{Limit: 3, Remaining: 3}) {
			t.Fatalf("empty status %+v", status)
		}
		mustAdmit(t, pools, key)
		for range 2 {
			if status := pools.Status(key); status.Remaining != 2 {
				t.Fatalf("status consumed a slot: %+v", status)
			}
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := pools.Admit(ctx, key); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if status := pools.Status(key); status.Remaining != 2 {
			t.Fatalf("cancelled request took a slot: %+v", status)
		}
	})
}

func TestRequestPoolSweepsIdlePools(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pools := NewRequestPools()
		mustAdmit(t, pools, pooledKey("idle", 5, 30))
		time.Sleep(time.Minute)
		mustAdmit(t, pools, pooledKey("busy", 5, 30))
		pools.mu.Lock()
		_, idle := pools.windows["idle"]
		_, busy := pools.windows["busy"]
		pools.mu.Unlock()
		if idle || !busy {
			t.Fatalf("idle kept %v, busy kept %v", idle, busy)
		}
	})
}
