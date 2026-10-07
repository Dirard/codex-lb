package upstream

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestWebSocketContinuationWaiterDoesNotBorrowSiblingOrStaleAnchor(t *testing.T) {
	for _, outcome := range []string{"advanced", "closed", "cancelled"} {
		t.Run(outcome, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				connection, err := websocket.Accept(w, r, nil)
				if err == nil {
					defer connection.CloseNow()
					if _, _, err := connection.Read(ctx); err == nil {
						t.Error("acquiring a lease must not dispatch response.create")
					}
				}
			}))
			defer server.Close()
			adapter := New(server.Client(), nil)
			defer adapter.Close()
			target := testTarget(server.URL, ResponsesCapabilities())
			target.SessionID = "shared-session"
			acquire := func(previous string) *websocketSession {
				t.Helper()
				entry, _, err := adapter.sessions.acquire(ctx, target, previous, server.Client())
				if err != nil {
					t.Fatal(err)
				}
				return entry
			}
			first := acquire("")
			adapter.sessions.release(first, true, "seed", true)
			if acquire("seed") != first {
				t.Fatal("exact owner changed")
			}
			sibling := acquire("")
			adapter.sessions.release(sibling, true, "sibling", true)
			waitCtx, stopWait := context.WithCancel(ctx)
			defer stopWait()
			done := make(chan error, 1)
			go func() {
				entry, _, err := adapter.sessions.acquire(waitCtx, target, "seed", server.Client())
				if err == nil {
					adapter.sessions.release(entry, true, "", true)
				}
				done <- err
			}()
			for {
				adapter.sessions.mu.Lock()
				waiting := first.refs == 2
				adapter.sessions.mu.Unlock()
				if waiting {
					break
				}
				select {
				case err := <-done:
					t.Fatalf("exact continuation skipped its busy socket: %v", err)
				case <-ctx.Done():
					t.Fatal("waiter was not registered")
				case <-time.After(time.Millisecond):
				}
			}
			wantCode := ErrorCodeContinuationNotFound
			switch outcome {
			case "advanced":
				adapter.sessions.release(first, true, "advanced", true)
			case "closed":
				adapter.sessions.release(first, false, "", true)
			case "cancelled":
				stopWait()
				wantCode = "request_cancelled"
			}
			var failure *Error
			if err := <-done; !errors.As(err, &failure) || failure.Code != wantCode || !failure.RejectedBeforeExecution {
				t.Fatalf("incorrect pre-dispatch result: %v", err)
			}
			if outcome == "cancelled" {
				adapter.sessions.release(first, true, "seed", true)
				if acquire("seed") != first {
					t.Fatal("cancelled waiter retired the active owner")
				}
				adapter.sessions.release(first, true, "seed", true)
			}
			if acquire("sibling") != sibling {
				t.Fatal("waiter damaged an independent sibling")
			}
			adapter.sessions.release(sibling, true, "sibling", true)
			cancelled, cancelNow := context.WithCancel(ctx)
			cancelNow()
			_, _, err := adapter.sessions.acquire(cancelled, target, "", server.Client())
			if !errors.As(err, &failure) || !failure.RejectedBeforeExecution || !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled fresh lease was acquired: %v", err)
			}
			if acquire("sibling") != sibling {
				t.Fatal("pre-dispatch cancellation destroyed retained state")
			}
			adapter.sessions.release(sibling, true, "sibling", true)
		})
	}
}

func TestRequiredCapabilityRetiresEveryIdleSibling(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := websocket.Accept(w, r, nil)
		if err == nil {
			defer connection.CloseNow()
			_, _, _ = connection.Read(ctx)
		}
	}))
	defer server.Close()
	adapter := New(server.Client(), nil)
	defer adapter.Close()
	target := testTarget(server.URL, ResponsesCapabilities())
	target.SessionID, target.RequiredCapability = "shared-required", true
	entries := make([]*websocketSession, 4)
	for i := range entries {
		entry, _, err := adapter.sessions.acquire(ctx, target, "", server.Client())
		if err != nil {
			t.Fatal(err)
		}
		entries[i] = entry
	}
	for i, entry := range entries[:3] {
		adapter.sessions.release(entry, true, fmt.Sprintf("response-%d", i), true)
	}
	adapter.RetireRequiredCapability(ownerOf(target))
	adapter.sessions.mu.Lock()
	remaining := adapter.sessions.lanes
	allIdleDead := entries[0].dead && entries[1].dead && entries[2].dead
	activeRetained := !entries[3].dead && entries[3].retireWhenIdle
	adapter.sessions.mu.Unlock()
	if remaining != 1 || !allIdleDead || !activeRetained {
		t.Fatalf("sibling retirement skipped an entry: lanes=%d idle=%v active=%v", remaining, allIdleDead, activeRetained)
	}
	adapter.sessions.release(entries[3], true, "last", true)
	adapter.sessions.mu.Lock()
	remaining = adapter.sessions.lanes + len(adapter.sessions.entries) + len(adapter.sessions.responses)
	adapter.sessions.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("retirement retained %d session records", remaining)
	}
}
