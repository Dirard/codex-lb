package application

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestProxyDefaultCapacityAndBoundedCancellation(t *testing.T) {
	defaultProxy := NewProxy(nil, nil, nil, ProxyConfig{})
	if cap(defaultProxy.streams) != 512 || cap(defaultProxy.admitted) != 640 || defaultProxy.config.QueueTimeout != 15*time.Second {
		t.Fatal("default stream capacity or bounded queue changed")
	}

	proxy := NewProxy(nil, nil, nil, ProxyConfig{MaxStreams: 1, MaxQueued: 1, QueueTimeout: 5 * time.Second})
	release, err := proxy.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waiter := make(chan error, 1)
	go func() {
		_, err := proxy.Acquire(ctx)
		waiter <- err
	}()
	deadline := time.After(2 * time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for len(proxy.admitted) != 2 {
		select {
		case <-deadline:
			t.Fatal("waiting request never entered the bounded queue")
		case <-ticker.C:
		}
	}
	if _, err := proxy.Acquire(context.Background()); err == nil {
		t.Fatal("full local queue admitted another request")
	} else {
		var capacity *ProxyError
		if !errors.As(err, &capacity) || capacity.Code != "local_capacity_exceeded" {
			t.Fatalf("overload error: %v", err)
		}
	}
	cancel()
	if err := <-waiter; !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting request did not cancel: %v", err)
	}
	release()
	if len(proxy.streams) != 0 || len(proxy.admitted) != 0 {
		t.Fatal("capacity tokens leaked after cancellation")
	}
	acquired, err := proxy.Acquire(context.Background())
	if err != nil {
		t.Fatalf("capacity did not recover: %v", err)
	}
	acquired()
}
