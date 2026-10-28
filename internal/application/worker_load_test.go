package application

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"codex-lb/internal/domain"
)

type admissionResult struct {
	release func()
	lease   *accountLease
	err     error
}

func peerSlots(p *Proxy) (streams, admitted int) {
	p.admissionMu.Lock()
	defer p.admissionMu.Unlock()
	return p.peerStreams, p.peerAdmitted
}

func pending(results <-chan admissionResult) (admissionResult, bool) {
	select {
	case result := <-results:
		return result, true
	default:
		return admissionResult{}, false
	}
}

func closed(signal <-chan struct{}) bool {
	select {
	case <-signal:
		return true
	default:
		return false
	}
}

func TestWorkerLoadAddDeepSumsWithoutAliasing(t *testing.T) {
	left := WorkerLoad{Active: 1, Queued: 2, Accounts: map[string]AccountLoad{
		"a": {Creates: 1, Streams: 2, Keys: map[string]int{"k1": 2}},
	}}
	right := WorkerLoad{Active: 3, Queued: 4, Accounts: map[string]AccountLoad{
		"a": {Creates: 2, Streams: 1, Keys: map[string]int{"k1": 1, "k2": 3}},
		"b": {Streams: 1},
	}}
	sum := left.Add(right)
	want := WorkerLoad{Active: 4, Queued: 6, Accounts: map[string]AccountLoad{
		"a": {Creates: 3, Streams: 3, Keys: map[string]int{"k1": 3, "k2": 3}},
		"b": {Streams: 1},
	}}
	if !reflect.DeepEqual(sum, want) {
		t.Fatalf("sum %+v, want %+v", sum, want)
	}
	sum.Accounts["a"].Keys["k1"] = 100
	sum.Accounts["c"] = AccountLoad{Streams: 9}
	if left.Accounts["a"].Keys["k1"] != 2 || right.Accounts["a"].Keys["k1"] != 1 || len(left.Accounts) != 1 || len(right.Accounts) != 2 {
		t.Fatalf("sum aliases its inputs: left=%+v right=%+v", left, right)
	}
	right.Accounts["a"].Keys["k2"] = 50
	right.Accounts["d"] = AccountLoad{Creates: 1}
	if sum.Accounts["a"].Keys["k2"] != 3 || len(sum.Accounts) != 3 {
		t.Fatalf("input change reached the sum: %+v", sum)
	}
}

func TestWorkerLoadAddLeavesInputsUntouched(t *testing.T) {
	keys := map[string]int{"k": 1}
	left := WorkerLoad{Accounts: map[string]AccountLoad{"a": {Streams: 1, Keys: keys}}}
	sum := left.Add(WorkerLoad{})
	keys["k"] = 7
	if sum.Accounts["a"].Keys["k"] != 1 {
		t.Fatal("sum shares the input's key map")
	}
	if empty := (WorkerLoad{}).Add(WorkerLoad{}); empty.Accounts != nil {
		t.Fatal("empty sum allocated accounts")
	}
	encoded, err := json.Marshal(WorkerLoad{})
	if err != nil || string(encoded) != `{"active":0,"queued":0}` {
		t.Fatalf("empty load wire form: %s %v", encoded, err)
	}
}

func TestPeerAccountStreamsHoldCapUntilDecrease(t *testing.T) {
	settings := domain.RuntimeSettings{RoutingStrategy: "round_robin", ProxyAccountStreamLimit: 3, ProxyAccountStreamRecoveryReserve: 1}
	p := admissionTestProxy()
	p.SetPeerLoad(WorkerLoad{Accounts: map[string]AccountLoad{"a": {Streams: 2}}})
	_, _, err := p.admittedAccount(context.Background(), admissionTestChoices("a"), settings, "key", false, true)
	var failure *ProxyError
	if !errors.As(err, &failure) || failure.Code != "account_stream_cap" || failure.Status != 429 {
		t.Fatalf("peer streams did not count against the soft stream limit: %v", err)
	}
	selected, spill, err := p.admittedAccount(context.Background(), admissionTestChoices("a", "b"), settings, "key", false, true)
	if err != nil || selected.account.ID != "b" {
		t.Fatalf("soft work did not spill from a peer-filled account: account=%s err=%v", selected.account.ID, err)
	}
	spill.release()
	_, recovery, err := p.admittedAccount(context.Background(), admissionTestChoices("a"), settings, "key", true, true)
	if err != nil {
		t.Fatalf("confirmed owner lost the recovery slot to peer streams: %v", err)
	}
	defer recovery.release()

	synctest.Test(t, func(t *testing.T) {
		p := NewProxy(nil, nil, nil, ProxyConfig{QueueTimeout: time.Minute})
		p.SetPeerLoad(WorkerLoad{Accounts: map[string]AccountLoad{"a": {Streams: 3}}})
		results := make(chan admissionResult, 1)
		go func() {
			_, lease, err := p.admittedAccount(context.Background(), admissionTestChoices("a"), settings, "key", true, true)
			results <- admissionResult{lease: lease, err: err}
		}()
		synctest.Wait()
		if result, done := pending(results); done {
			t.Fatalf("peer streams at the account cap did not hold admission: %v", result.err)
		}
		// Another account's change wakes the waiter, which re-checks and keeps waiting.
		p.SetPeerLoad(WorkerLoad{Accounts: map[string]AccountLoad{"a": {Streams: 3}, "b": {Streams: 1}}})
		synctest.Wait()
		if result, done := pending(results); done {
			t.Fatalf("unrelated peer change admitted a full account: %v", result.err)
		}
		p.SetPeerLoad(WorkerLoad{Accounts: map[string]AccountLoad{"a": {Streams: 2}}})
		synctest.Wait()
		result, done := pending(results)
		if !done || result.err != nil {
			t.Fatalf("peer decrease did not wake the waiting admission: done=%v err=%v", done, result.err)
		}
		result.lease.release()
	})
}

func TestPeerAccountCreatesCountAgainstCreateCap(t *testing.T) {
	settings := domain.RuntimeSettings{RoutingStrategy: "round_robin", ProxyAccountResponseCreateLimit: 2, ProxyAccountStreamLimit: 10}
	p := admissionTestProxy()
	_, own, err := p.admittedAccount(context.Background(), admissionTestChoices("a"), settings, "key", true, true)
	if err != nil {
		t.Fatal(err)
	}
	defer own.release()
	p.SetPeerLoad(WorkerLoad{Accounts: map[string]AccountLoad{"a": {Creates: 1, Streams: 1}}})
	_, _, err = p.admittedAccount(context.Background(), admissionTestChoices("a"), settings, "key", true, true)
	var failure *ProxyError
	if !errors.As(err, &failure) || failure.Code != "account_response_create_cap" {
		t.Fatalf("peer creates did not count against the create cap: %v", err)
	}
	// A peer's first upstream event releases its create while the stream stays.
	p.SetPeerLoad(WorkerLoad{Accounts: map[string]AccountLoad{"a": {Streams: 1}}})
	_, next, err := p.admittedAccount(context.Background(), admissionTestChoices("a"), settings, "key", true, true)
	if err != nil {
		t.Fatalf("released peer create still held the account: %v", err)
	}
	next.release()
}

func TestPeerKeyStreamsCountInFairShare(t *testing.T) {
	settings := domain.RuntimeSettings{RoutingStrategy: "round_robin", ProxyAccountStreamLimit: 4,
		ProxyApiKeyFairShareCongestionThresholdPct: 50}
	p := admissionTestProxy()
	p.SetPeerLoad(WorkerLoad{Accounts: map[string]AccountLoad{
		"a": {Streams: 3, Keys: map[string]int{"heavy": 3}},
		"b": {Streams: 2, Keys: map[string]int{"heavy": 1, "other": 1, "zero": 0}},
	}})
	choices := admissionTestChoices("a", "b")
	a := p.accountAdmission
	a.mu.Lock()
	heavy, share, inflight, capacity, activeKeys, denied := a.fairShareLocked(choices, settings, "heavy")
	light, lightShare, _, _, lightKeys, lightDenied := a.fairShareLocked(choices, settings, "light")
	a.mu.Unlock()
	if !denied || heavy != 4 || share != 4 || inflight != 5 || capacity != 8 || activeKeys != 2 {
		t.Fatalf("peer key streams missing from fair share: heavy=%d share=%d pool=%d/%d keys=%d denied=%v",
			heavy, share, inflight, capacity, activeKeys, denied)
	}
	if light != 0 || lightShare != 2 || lightKeys != 3 || lightDenied {
		t.Fatalf("light key judged by peer pressure: light=%d share=%d keys=%d denied=%v", light, lightShare, lightKeys, lightDenied)
	}
	_, _, err := p.admittedAccount(context.Background(), choices, settings, "heavy", false, true)
	var failure *ProxyError
	if !errors.As(err, &failure) || failure.Code != "api_key_stream_fair_share" ||
		failure.Message != "API key holds 4 streams of fair share 4 (5/8 pool streams in flight across 2 active keys)" {
		t.Fatalf("fair share ignored peer streams: %v", err)
	}
	p.SetPeerLoad(WorkerLoad{})
	_, lease, err := p.admittedAccount(context.Background(), choices, settings, "heavy", false, true)
	if err != nil {
		t.Fatalf("cleared peer load still denied fair share: %v", err)
	}
	lease.release()
}

func TestPeerLoadHoldsGlobalCapacityUntilCleared(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		p := NewProxy(nil, nil, nil, ProxyConfig{MaxStreams: 2, MaxQueued: 1, QueueTimeout: time.Minute})
		p.SetPeerLoad(WorkerLoad{Active: 2})
		if !p.TryBeginIdleDrain() || !p.ResumeAfterIdleDrain() {
			t.Fatal("peer streams blocked the local idle gate")
		}
		results := make(chan admissionResult, 1)
		go func() {
			release, err := p.acquire(ctx)
			results <- admissionResult{release: release, err: err}
		}()
		synctest.Wait()
		if result, done := pending(results); done {
			t.Fatalf("peer streams did not hold local stream capacity: %v", result.err)
		}
		if active, queued := p.AdmissionUsage(); active != 0 || queued != 1 {
			t.Fatalf("usage counted peer slots: active=%d queued=%d", active, queued)
		}
		// Two peer generations and one local queued admission fill the queue.
		_, err := p.acquire(ctx)
		var failure *ProxyError
		if !errors.As(err, &failure) || failure.Code != "local_capacity_exceeded" || failure.Message != "Request queue is full; retry shortly" {
			t.Fatalf("peer admissions did not count against the queue: %v", err)
		}
		p.SetPeerLoad(WorkerLoad{})
		synctest.Wait()
		result, done := pending(results)
		if !done || result.err != nil {
			t.Fatalf("cleared peer load did not admit the queued request: done=%v err=%v", done, result.err)
		}
		if active, queued := p.AdmissionUsage(); active != 1 || queued != 0 {
			t.Fatalf("usage after clear: active=%d queued=%d", active, queued)
		}
		result.release()
		if len(p.streams) != 0 || len(p.admitted) != 0 {
			t.Fatalf("slots leaked: streams=%d admitted=%d", len(p.streams), len(p.admitted))
		}

		// Peer streams keep the timeout envelope of a full local server.
		p.SetPeerLoad(WorkerLoad{Active: 2})
		_, err = p.acquire(ctx)
		if !errors.As(err, &failure) || failure.Code != "local_capacity_exceeded" || failure.Message != "Timed out waiting for local stream capacity" {
			t.Fatalf("waiting behind peer streams: %v", err)
		}
		p.SetPeerLoad(WorkerLoad{Queued: 5})
		if streams, admitted := peerSlots(p); streams != 0 || admitted != 3 {
			t.Fatalf("peer queue slots: streams=%d admitted=%d", streams, admitted)
		}
		if _, err = p.acquire(ctx); !errors.As(err, &failure) || failure.Message != "Request queue is full; retry shortly" {
			t.Fatalf("peer queue did not fill local admission: %v", err)
		}
		p.SetPeerLoad(WorkerLoad{})
		if len(p.streams) != 0 || len(p.admitted) != 0 {
			t.Fatalf("clear left peer slots: streams=%d admitted=%d", len(p.streams), len(p.admitted))
		}
	})
}

func TestPeerSlotsTopUpWhenLocalCapacityFrees(t *testing.T) {
	ctx := context.Background()
	p := NewProxy(nil, nil, nil, ProxyConfig{MaxStreams: 2, MaxQueued: 1, QueueTimeout: time.Millisecond})
	first, err := p.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p.SetPeerLoad(WorkerLoad{Active: 2})
	if streams, admitted := peerSlots(p); streams != 0 || admitted != 1 {
		t.Fatalf("full channels: peer streams=%d admitted=%d", streams, admitted)
	}
	if got := p.Load(); got.Active != 2 || got.Queued != 0 {
		t.Fatalf("own load with partial peer slots: %+v", got)
	}
	first()
	p.SetPeerLoad(WorkerLoad{Active: 2})
	if streams, admitted := peerSlots(p); streams != 1 || admitted != 2 {
		t.Fatalf("freed slot was not topped up: peer streams=%d admitted=%d", streams, admitted)
	}
	var failure *ProxyError
	if _, err := p.acquire(ctx); !errors.As(err, &failure) || failure.Message != "Request queue is full; retry shortly" {
		t.Fatalf("admission while own work and peer slots fill capacity: %v", err)
	}
	second()
	p.SetPeerLoad(WorkerLoad{Active: 1})
	if streams, admitted := peerSlots(p); streams != 1 || admitted != 1 {
		t.Fatalf("decrease: peer streams=%d admitted=%d", streams, admitted)
	}
	if !p.TryBeginIdleDrain() {
		t.Fatal("peer slots kept the idle gate closed")
	}
}

func TestLoadReportsOnlyOwnAdmittedWork(t *testing.T) {
	ctx := context.Background()
	settings := domain.RuntimeSettings{RoutingStrategy: "round_robin", ProxyAccountResponseCreateLimit: 10, ProxyAccountStreamLimit: 10}
	p := admissionTestProxy()
	release, err := p.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, lease, err := p.admittedAccount(ctx, admissionTestChoices("a"), settings, "key", false, true)
	if err != nil {
		t.Fatal(err)
	}
	p.SetPeerLoad(WorkerLoad{Active: 600, Queued: 50, Accounts: map[string]AccountLoad{
		"a": {Creates: 3, Streams: 4, Keys: map[string]int{"key": 2, "other": 2}},
		"b": {Streams: 1},
	}})
	want := WorkerLoad{Active: 1, Accounts: map[string]AccountLoad{"a": {Creates: 1, Streams: 1, Keys: map[string]int{"key": 1}}}}
	if got := p.Load(); !reflect.DeepEqual(got, want) {
		t.Fatalf("load %+v, want own work %+v", got, want)
	}
	lease.releaseCreate()
	want.Accounts["a"] = AccountLoad{Streams: 1, Keys: map[string]int{"key": 1}}
	if got := p.Load(); !reflect.DeepEqual(got, want) {
		t.Fatalf("load after first event %+v, want %+v", got, want)
	}
	snapshot := p.Load()
	snapshot.Accounts["a"].Keys["key"] = 99
	if got := p.Load(); !reflect.DeepEqual(got, want) {
		t.Fatalf("load snapshot aliases admission state: %+v", got)
	}
	lease.release()
	release()
	if got := p.Load(); !reflect.DeepEqual(got, WorkerLoad{}) {
		t.Fatalf("idle process reports load %+v", got)
	}
	p.SetPeerLoad(WorkerLoad{})
	if len(p.streams) != 0 || len(p.admitted) != 0 {
		t.Fatalf("slots leaked: streams=%d admitted=%d", len(p.streams), len(p.admitted))
	}
}

func TestPeerLoadConcurrentWithAdmissionAndDrain(t *testing.T) {
	ctx := context.Background()
	settings := domain.RuntimeSettings{RoutingStrategy: "round_robin", ProxyAccountResponseCreateLimit: 2, ProxyAccountStreamLimit: 2,
		ProxyApiKeyFairShareCongestionThresholdPct: 50}
	p := NewProxy(nil, nil, nil, ProxyConfig{MaxStreams: 4, MaxQueued: 4, QueueTimeout: time.Millisecond})
	choices := admissionTestChoices("a", "b")
	var workers sync.WaitGroup
	for worker := range 8 {
		workers.Go(func() {
			for range 200 {
				if release, err := p.acquire(ctx); err == nil {
					if _, lease, err := p.admittedAccount(ctx, choices, settings, []string{"k1", "k2"}[worker%2], false, true); err == nil {
						lease.releaseCreate()
						lease.release()
					}
					release()
				}
			}
		})
	}
	for i := range 2000 {
		p.SetPeerLoad(WorkerLoad{Active: i % 6, Queued: i % 3, Accounts: map[string]AccountLoad{
			"a": {Creates: i % 2, Streams: i % 3, Keys: map[string]int{"k1": i % 3}},
		}})
		_ = p.Load()
		if i == 1000 {
			p.BeginDrain()
		}
	}
	workers.Wait()
	p.SetPeerLoad(WorkerLoad{})
	if len(p.streams) != 0 || len(p.admitted) != 0 {
		t.Fatalf("slots leaked: streams=%d admitted=%d", len(p.streams), len(p.admitted))
	}
	if got := p.Load(); !reflect.DeepEqual(got, WorkerLoad{}) {
		t.Fatalf("account pressure leaked: %+v", got)
	}
	p.accountAdmission.mu.Lock()
	defer p.accountAdmission.mu.Unlock()
	if len(p.accountAdmission.waiters) != 0 || p.accountAdmission.peers != nil {
		t.Fatal("waiters or peer pressure leaked")
	}
}

func TestRetirementKeepsAdmissionOpenAndClosesSessions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		p := NewProxy(nil, nil, nil, ProxyConfig{MaxStreams: 1, MaxQueued: 1, QueueTimeout: time.Minute})
		active, err := p.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		results := make(chan admissionResult, 1)
		go func() {
			release, err := p.Acquire(ctx)
			results <- admissionResult{release: release, err: err}
		}()
		synctest.Wait()
		if closed(p.SessionsClosing()) {
			t.Fatal("sessions closing before retirement")
		}
		p.BeginRetire()
		p.BeginRetire()
		if !closed(p.SessionsClosing()) || closed(p.DrainStarted()) {
			t.Fatal("retirement must close sessions without draining")
		}
		synctest.Wait()
		if result, done := pending(results); done {
			t.Fatalf("retirement ended a queued admission: %v", result.err)
		}
		active()
		synctest.Wait()
		result, done := pending(results)
		if !done || result.err != nil {
			t.Fatalf("queued admission after retirement: done=%v err=%v", done, result.err)
		}
		result.release()
		release, err := p.Acquire(ctx)
		if err != nil {
			t.Fatalf("retirement rejected a new admission: %v", err)
		}
		release()
		p.BeginDrain()
		_, err = p.Acquire(ctx)
		var failure *ProxyError
		if !errors.As(err, &failure) || failure.Code != "server_draining" {
			t.Fatalf("drain after retirement: %v", err)
		}
	})
}

func TestSessionsClosingFollowsDrain(t *testing.T) {
	p := NewProxy(nil, nil, nil, ProxyConfig{})
	if !p.TryBeginIdleDrain() || !closed(p.SessionsClosing()) {
		t.Fatal("idle drain kept sessions open")
	}
	if !p.ResumeAfterIdleDrain() || closed(p.SessionsClosing()) {
		t.Fatal("cancelled idle drain kept sessions closing")
	}
	p.BeginRetire()
	if !p.TryBeginIdleDrain() || !p.ResumeAfterIdleDrain() || !closed(p.SessionsClosing()) {
		t.Fatal("resumed idle drain reopened a retiring process's sessions")
	}
	drained := NewProxy(nil, nil, nil, ProxyConfig{})
	drained.BeginDrain()
	if !closed(drained.SessionsClosing()) {
		t.Fatal("drain kept sessions open")
	}
}
