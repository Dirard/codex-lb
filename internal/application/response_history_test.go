package application

import (
	"errors"
	"testing"
)

func TestLazyHistoryLoadsOnceAndJoins(t *testing.T) {
	calls := 0
	h := &lazyHistory{load: func() (responseContext, error) {
		calls++
		return responseContext{Items: nil}, errors.New("unreadable")
	}}
	h.join() // Not started: nothing to wait for.
	h.start()
	h.start()
	h.join()
	if _, err := h.get(); err == nil || calls != 1 {
		t.Fatalf("history loaded %d times, err=%v", calls, err)
	}
	var absent *lazyHistory
	absent.start()
	absent.join()
	if history, err := absent.get(); err != nil || len(history.Items) != 0 {
		t.Fatalf("absent history: %+v %v", history, err)
	}
	panicking := &lazyHistory{load: func() (responseContext, error) { panic("decrypt") }}
	if _, err := panicking.get(); !errors.Is(err, errInvalidStoredContinuation) {
		t.Fatalf("panicking load: %v", err)
	}
}
