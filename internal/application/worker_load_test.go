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

// A key waiting for an account on the other worker is an active key of the
// union: fair share over two workers must judge a busy key as one process
// holding both would, and forget the key once its admission stops waiting.
func TestPeerWaitingKeysCountInFairShare(t *testing.T) {
	settings := domain.RuntimeSettings{RoutingStrategy: "round_robin", ProxyAccountStreamLimit: 4,
		ProxyApiKeyFairShareCongestionThresholdPct: 50}
	choices := admissionTestChoices("a")
	fairShare := func(p *Proxy) (share, keys int, denied bool) {
		a := p.accountAdmission
		a.mu.Lock()
		defer a.mu.Unlock()
		_, share, _, _, keys, denied = a.fairShareLocked(choices, settings, "heavy")
		return share, keys, denied
	}
	wait := func(p *Proxy, key string) uint64 {
		a := p.accountAdmission
		a.mu.Lock()
		defer a.mu.Unlock()
		var id uint64
		a.waitLocked(&id, key, choices)
		return id
	}
	hold := func(p *Proxy) {
		for range 2 {
			if _, _, err := p.admittedAccount(context.Background(), choices, settings, "heavy", false, true); err != nil {
				t.Fatal(err)
			}
		}
	}

	single := admissionTestProxy()
	hold(single)
	wait(single, "starved")
	singleShare, singleKeys, singleDenied := fairShare(single)
	if singleShare != 2 || singleKeys != 2 || !singleDenied {
		t.Fatalf("one process: share=%d keys=%d denied=%v", singleShare, singleKeys, singleDenied)
	}

	active, retiring := admissionTestProxy(), admissionTestProxy()
	hold(active)
	waiter := wait(retiring, "starved")
	wait(retiring, "starved")
	wait(retiring, "")
	if load := retiring.Load(); !reflect.DeepEqual(load, WorkerLoad{Accounts: map[string]AccountLoad{"a": {Waiting: []string{"starved"}}}}) {
		t.Fatalf("waiting keys reported as %+v", load)
	}
	relay := func() { la, lr := active.Load(), retiring.Load(); active.SetPeerLoad(lr); retiring.SetPeerLoad(la) }
	relay()
	if share, keys, denied := fairShare(active); share != singleShare || keys != singleKeys || denied != singleDenied {
		t.Fatalf("two workers: share=%d keys=%d denied=%v, one process: share=%d keys=%d denied=%v",
			share, keys, denied, singleShare, singleKeys, singleDenied)
	}
	retiring.accountAdmission.removeWaiter(waiter)
	relay()
	if share, keys, denied := fairShare(active); keys != 2 || !denied || share != 2 {
		t.Fatalf("a key still waiting was forgotten: share=%d keys=%d denied=%v", share, keys, denied)
	}
	retiring.accountAdmission.mu.Lock()
	for id := range retiring.accountAdmission.waiters {
		delete(retiring.accountAdmission.waiters, id)
	}
	retiring.accountAdmission.mu.Unlock()
	relay()
	if share, keys, denied := fairShare(active); keys != 1 || denied || share != 4 {
		t.Fatalf("a key that stopped waiting still counts: share=%d keys=%d denied=%v", share, keys, denied)
	}
}

func TestWorkerLoadAddUnitesWaitingKeys(t *testing.T) {
	left := WorkerLoad{Accounts: map[string]AccountLoad{"a": {Streams: 1, Waiting: []string{"k2", "k1"}}}}
	right := WorkerLoad{Accounts: map[string]AccountLoad{"a": {Waiting: []string{"k1", "k3", ""}}, "b": {Waiting: []string{"k1"}}}}
	sum := left.Add(right)
	want := WorkerLoad{Accounts: map[string]AccountLoad{"a": {Streams: 1, Waiting: []string{"k1", "k2", "k3"}}, "b": {Waiting: []string{"k1"}}}}
	if !reflect.DeepEqual(sum, want) {
		t.Fatalf("sum %+v, want %+v", sum, want)
	}
	sum.Accounts["b"].Waiting[0] = "changed"
	if right.Accounts["b"].Waiting[0] != "k1" || left.Accounts["a"].Waiting[0] != "k2" {
		t.Fatalf("sum aliases its inputs: left=%+v right=%+v", left, right)
	}
	encoded, err := json.Marshal(want.Accounts["b"])
	if err != nil || string(encoded) != `{"creates":0,"streams":0,"waiting":["k1"]}` {
		t.Fatalf("waiting keys wire form: %s %v", encoded, err)
	}
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

// Both workers admitted up to their own capacity before the first relay and
// keep a backlog. A slot freed while the peers' load wants more peer tokens
// than this worker holds becomes a peer token instead of going to a local
// waiter, so the union is back within the plan after one relay rather than
// once the backlog is gone; clearing the peer load hands the slots back.
func TestReleasedSlotsBecomePeerTokensBeforeLocalWaiters(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel() // A failure must not leave queued admissions behind.
		workers := []*Proxy{
			NewProxy(nil, nil, nil, ProxyConfig{MaxStreams: 2, MaxQueued: 4, QueueTimeout: time.Minute}),
			NewProxy(nil, nil, nil, ProxyConfig{MaxStreams: 2, MaxQueued: 4, QueueTimeout: time.Minute}),
		}
		results := make([]chan admissionResult, len(workers))
		held := make([][]func(), len(workers))
		for i, p := range workers {
			results[i] = make(chan admissionResult, 4)
			for range 4 {
				go func() {
					release, err := p.acquire(ctx)
					results[i] <- admissionResult{release: release, err: err}
				}()
			}
		}
		admitted := func(i, want int) {
			t.Helper()
			synctest.Wait()
			for range want {
				result, done := pending(results[i])
				if !done || result.err != nil {
					t.Fatalf("worker %d: queued admission did not start: done=%v err=%v", i, done, result.err)
				}
				held[i] = append(held[i], result.release)
			}
			if result, done := pending(results[i]); done {
				t.Fatalf("worker %d: a queued admission took a slot held for the peer: %v", i, result.err)
			}
		}
		relay := func() {
			first, second := workers[0].Load(), workers[1].Load()
			workers[0].SetPeerLoad(second)
			workers[1].SetPeerLoad(first)
		}
		union := func() int { return workers[0].Load().Active + workers[1].Load().Active }
		admitted(0, 2)
		admitted(1, 2)
		relay()
		if got := union(); got != 4 {
			t.Fatalf("union before any release = %d", got)
		}
		release := func(i int) {
			held[i][0]()
			held[i] = held[i][1:]
		}
		// One generation finishes on each worker; both keep two queued.
		for i := range workers {
			release(i)
			admitted(i, 0)
		}
		if got := union(); got != 2 {
			t.Fatalf("union after one release per worker = %d, want the plan of 2", got)
		}
		for i, p := range workers {
			if streams, slots := peerSlots(p); streams != 1 || slots != 3 {
				t.Fatalf("worker %d peer tokens: streams=%d admitted=%d", i, streams, slots)
			}
			if active, queued := p.AdmissionUsage(); active != 1 || queued != 2 {
				t.Fatalf("worker %d own usage: active=%d queued=%d", i, active, queued)
			}
		}
		relay()
		// The peer tokens now match the peers' load, so the next release
		// admits a local waiter and the union stays at the plan.
		release(0)
		admitted(0, 1)
		if got := union(); got != 2 {
			t.Fatalf("union after a release at the wanted peer tokens = %d", got)
		}
		workers[0].SetPeerLoad(WorkerLoad{})
		admitted(0, 1)
		if active, queued := workers[0].AdmissionUsage(); active != 2 || queued != 0 {
			t.Fatalf("cleared peer load: active=%d queued=%d", active, queued)
		}
		workers[1].SetPeerLoad(WorkerLoad{})
		admitted(1, 1)
		for i, p := range workers {
			for len(held[i]) != 0 {
				release(i)
				synctest.Wait()
				if result, done := pending(results[i]); done {
					if result.err != nil {
						t.Fatalf("worker %d: last queued admission: %v", i, result.err)
					}
					held[i] = append(held[i], result.release)
				}
			}
			if len(p.streams) != 0 || len(p.admitted) != 0 {
				t.Fatalf("worker %d slots leaked: streams=%d admitted=%d", i, len(p.streams), len(p.admitted))
			}
		}
	})
}

// A queued admission that gives up returns its admission slot to the peers'
// tally while their load wants it, like a finished generation does.
func TestTimedOutAdmissionLeavesItsSlotToThePeer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		p := NewProxy(nil, nil, nil, ProxyConfig{MaxStreams: 1, MaxQueued: 1, QueueTimeout: time.Second})
		active, err := p.acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		results := make(chan admissionResult, 1)
		go func() {
			release, err := p.acquire(ctx)
			results <- admissionResult{release: release, err: err}
		}()
		synctest.Wait()
		p.SetPeerLoad(WorkerLoad{Queued: 1})
		if streams, admitted := peerSlots(p); streams != 0 || admitted != 0 {
			t.Fatalf("full channels took peer tokens: streams=%d admitted=%d", streams, admitted)
		}
		time.Sleep(time.Second)
		synctest.Wait()
		var failure *ProxyError
		if result, done := pending(results); !done || !errors.As(result.err, &failure) || failure.Message != "Timed out waiting for local stream capacity" {
			t.Fatalf("queued admission: done=%v err=%v", done, result.err)
		}
		if streams, admitted := peerSlots(p); streams != 0 || admitted != 1 {
			t.Fatalf("timed-out slot: peer streams=%d admitted=%d", streams, admitted)
		}
		if _, err := p.acquire(ctx); !errors.As(err, &failure) || failure.Message != "Request queue is full; retry shortly" {
			t.Fatalf("the peer's queue slot went to a new local admission: %v", err)
		}
		active()
		p.SetPeerLoad(WorkerLoad{})
		if len(p.streams) != 0 || len(p.admitted) != 0 {
			t.Fatalf("slots leaked: streams=%d admitted=%d", len(p.streams), len(p.admitted))
		}
	})
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
