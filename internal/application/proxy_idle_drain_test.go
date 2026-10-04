package application_test

import (
	"context"
	"errors"
	"testing"

	"codex-lb/internal/application"
)

func TestTryBeginIdleDrainKeepsActiveAdmissionOpen(t *testing.T) {
	proxy := application.NewProxy(nil, nil, nil, application.ProxyConfig{})
	release, err := proxy.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if proxy.TryBeginIdleDrain() {
		t.Fatal("active work was drained")
	}
	select {
	case <-proxy.DrainStarted():
		t.Fatal("failed idle check closed admission")
	default:
	}
	release()
	if !proxy.TryBeginIdleDrain() {
		t.Fatal("idle admission was not drained")
	}
	if proxy.TryBeginIdleDrain() {
		t.Fatal("already draining admission passed the idle gate again")
	}
	_, err = proxy.Acquire(context.Background())
	var failure *application.ProxyError
	if !errors.As(err, &failure) || failure.Code != "server_draining" {
		t.Fatalf("new work after drain: %v", err)
	}
	if !proxy.ResumeAfterIdleDrain() {
		t.Fatal("cancelled update did not reopen admission")
	}
	release, err = proxy.Acquire(context.Background())
	if err != nil {
		t.Fatalf("new work after resume: %v", err)
	}
	release()
	proxy.BeginDrain()
	if proxy.ResumeAfterIdleDrain() {
		t.Fatal("permanent shutdown was reopened")
	}
}

func TestTryBeginIdleDrainRacesWithAdmission(t *testing.T) {
	for range 100 {
		proxy := application.NewProxy(nil, nil, nil, application.ProxyConfig{})
		start := make(chan struct{})
		acquired := make(chan func(), 1)
		gate := make(chan bool, 1)
		go func() {
			<-start
			release, _ := proxy.Acquire(context.Background())
			acquired <- release
		}()
		go func() {
			<-start
			gate <- proxy.TryBeginIdleDrain()
		}()
		close(start)
		release, drained := <-acquired, <-gate
		if drained == (release != nil) {
			t.Fatal("admission and idle drain both won, or both lost")
		}
		if release != nil {
			release()
		}
	}
}

func TestResumeAfterIdleDrainRacesWithAdmission(t *testing.T) {
	for range 100 {
		proxy := application.NewProxy(nil, nil, nil, application.ProxyConfig{})
		if !proxy.TryBeginIdleDrain() {
			t.Fatal("idle gate failed")
		}
		start := make(chan struct{})
		acquired := make(chan func(), 1)
		resumed := make(chan bool, 1)
		go func() {
			<-start
			release, _ := proxy.Acquire(context.Background())
			acquired <- release
		}()
		go func() {
			<-start
			resumed <- proxy.ResumeAfterIdleDrain()
		}()
		close(start)
		if release := <-acquired; release != nil {
			release()
		}
		if !<-resumed {
			t.Fatal("update drain did not resume")
		}
		release, err := proxy.Acquire(context.Background())
		if err != nil {
			t.Fatalf("new admission after resume: %v", err)
		}
		release()
	}
}
