package application

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

func admissionTestChoices(ids ...string) []accountCandidate {
	choices := make([]accountCandidate, 0, len(ids))
	for _, id := range ids {
		choices = append(choices, accountCandidate{account: domain.Account{ID: id, Kind: domain.AccountChatGPT, RoutingPolicy: "normal"}})
	}
	return choices
}

func admissionTestProxy() *Proxy {
	return NewProxy(nil, nil, nil, ProxyConfig{QueueTimeout: 25 * time.Millisecond})
}

func TestAccountAdmissionSpillAndRecoveryReserve(t *testing.T) {
	p := admissionTestProxy()
	settings := domain.RuntimeSettings{RoutingStrategy: "round_robin", ProxyAccountResponseCreateLimit: 4,
		ProxyAccountStreamLimit: 3, ProxyAccountStreamRecoveryReserve: 1}
	choices := admissionTestChoices("a")
	_, first, err := p.admittedAccount(context.Background(), choices, settings, "key", false, true)
	if err != nil {
		t.Fatal(err)
	}
	defer first.release()
	_, second, err := p.admittedAccount(context.Background(), choices, settings, "key", false, true)
	if err != nil {
		t.Fatal(err)
	}
	defer second.release()
	selected, spill, err := p.admittedAccount(context.Background(), admissionTestChoices("a", "b"), settings, "key", false, true)
	if err != nil || selected.account.ID != "b" {
		t.Fatalf("soft work did not spill from reserved slot: account=%s err=%v", selected.account.ID, err)
	}
	defer spill.release()
	selected, recovery, err := p.admittedAccount(context.Background(), choices, settings, "key", true, true)
	if err != nil || selected.account.ID != "a" {
		t.Fatalf("confirmed owner lost recovery slot: account=%s err=%v", selected.account.ID, err)
	}
	defer recovery.release()
	_, _, err = p.admittedAccount(context.Background(), choices, settings, "key", true, true)
	var failure *ProxyError
	if !errors.As(err, &failure) || failure.Code != "account_stream_cap" || failure.Status != 429 {
		t.Fatalf("hard owner exceeded stream cap: %v", err)
	}
}

func TestAccountCreateLeaseReleasesBeforeStreamLease(t *testing.T) {
	p := admissionTestProxy()
	settings := domain.RuntimeSettings{RoutingStrategy: "round_robin", ProxyAccountResponseCreateLimit: 1,
		ProxyAccountStreamLimit: 3}
	choices := admissionTestChoices("a")
	_, first, err := p.admittedAccount(context.Background(), choices, settings, "key", false, true)
	if err != nil {
		t.Fatal(err)
	}
	defer first.release()
	selected, spill, err := p.admittedAccount(context.Background(), admissionTestChoices("a", "b"), settings, "key", false, true)
	if err != nil || selected.account.ID != "b" {
		t.Fatalf("create cap did not spill: account=%s err=%v", selected.account.ID, err)
	}
	defer spill.release()
	first.releaseCreate()
	_, next, err := p.admittedAccount(context.Background(), choices, settings, "key", false, true)
	if err != nil {
		t.Fatalf("first actual event did not release create slot: %v", err)
	}
	next.release()
	p.accountAdmission.mu.Lock()
	streams := p.accountAdmission.accounts["a"].streams
	p.accountAdmission.mu.Unlock()
	if streams != 1 {
		t.Fatalf("create release also released active stream: %d", streams)
	}
}

func TestAccountAdmissionFairShareCountsWaitingDemand(t *testing.T) {
	p := admissionTestProxy()
	settings := domain.RuntimeSettings{RoutingStrategy: "round_robin", ProxyAccountStreamLimit: 4,
		ProxyApiKeyFairShareCongestionThresholdPct: 50}
	a := p.accountAdmission
	a.mu.Lock()
	a.accounts["a"] = &accountPressure{streams: 3, keys: map[string]int{"heavy": 3}}
	a.waiters[1] = accountWaiter{key: "light", accounts: map[string]bool{"a": true}}
	heavy, share, inflight, capacity, activeKeys, denied := a.fairShareLocked(admissionTestChoices("a"), settings, "heavy")
	light, _, _, _, _, lightDenied := a.fairShareLocked(admissionTestChoices("a"), settings, "light")
	a.mu.Unlock()
	if !denied || heavy != 3 || share != 2 || inflight != 3 || capacity != 4 || activeKeys != 2 || light != 0 || lightDenied {
		t.Fatalf("waiting key did not reserve a fair share: heavy=%d share=%d pool=%d/%d denied=%v light=%d lightDenied=%v",
			heavy, share, inflight, capacity, denied, light, lightDenied)
	}
}

func TestAccountAdmissionCancelledWaiterDoesNotLeak(t *testing.T) {
	p := admissionTestProxy()
	settings := domain.RuntimeSettings{RoutingStrategy: "round_robin", ProxyAccountStreamLimit: 1}
	choices := admissionTestChoices("a")
	_, held, err := p.admittedAccount(context.Background(), choices, settings, "first", false, true)
	if err != nil {
		t.Fatal(err)
	}
	defer held.release()
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() {
		_, _, err := p.admittedAccount(ctx, choices, settings, "second", false, true)
		finished <- err
	}()
	deadline := time.After(time.Second)
	for {
		p.accountAdmission.mu.Lock()
		waiting := len(p.accountAdmission.waiters)
		p.accountAdmission.mu.Unlock()
		if waiting == 1 {
			break
		}
		select {
		case <-deadline:
			cancel()
			t.Fatal("second request did not enter bounded wait")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("capacity wait ignored cancellation: %v", err)
	}
	p.accountAdmission.mu.Lock()
	defer p.accountAdmission.mu.Unlock()
	if len(p.accountAdmission.waiters) != 0 {
		t.Fatal("cancelled fair-share demand remained registered")
	}
}

type admissionSettingsStore struct {
	ProxyStore
	settings domain.RuntimeSettings
}

func (s admissionSettingsStore) LoadSettings(context.Context) (domain.RuntimeSettings, error) {
	return s.settings, nil
}

type admissionResponseProvider func(context.Context, ResponseTarget, json.RawMessage, func(ResponseEvent) error) (ResponseResult, error)

func (f admissionResponseProvider) Respond(ctx context.Context, target ResponseTarget, body json.RawMessage, emit func(ResponseEvent) error) (ResponseResult, error) {
	return f(ctx, target, body, emit)
}

func TestAdminWarmupUsesSharedAccountStreamCap(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	provider := admissionResponseProvider(func(_ context.Context, target ResponseTarget, _ json.RawMessage, _ func(ResponseEvent) error) (ResponseResult, error) {
		calls.Add(1)
		if target.OnFirstUpstreamEvent != nil {
			target.OnFirstUpstreamEvent()
		}
		close(entered)
		<-release
		return ResponseResult{ResponseID: "warmup_done", UsageKnown: true}, nil
	})
	accounts := newFakeAccounts()
	account := warmupAccount("warmup-account", domain.AccountActive)
	if err := accounts.SaveAccount(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	service := NewWarmupService(accounts, provider, &stubLedger{}, &stubAdmission{}, nil, time.Now)
	settings := domain.RuntimeSettings{RoutingStrategy: "round_robin", ProxyAccountResponseCreateLimit: 1,
		ProxyAccountStreamLimit: 1, ProxyAccountStreamRecoveryReserve: 0}
	proxy := NewProxy(admissionSettingsStore{settings: settings}, nil, nil, ProxyConfig{QueueTimeout: 25 * time.Millisecond})
	service.ConfigureAccountAdmission(proxy)
	finished := make(chan error, 1)
	go func() {
		finished <- service.ExecuteWarmup(context.Background(), account.ID, "gpt-5.4-mini", nil, "Say OK.")
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("first warmup did not reach provider")
	}
	err := service.ExecuteWarmup(context.Background(), account.ID, "gpt-5.4-mini", nil, "Say OK.")
	var capacity *ProxyError
	close(release)
	if !errors.As(err, &capacity) || capacity.Code != "account_stream_cap" || calls.Load() != 1 || <-finished != nil {
		t.Fatalf("synthetic warmup bypassed shared cap: error=%v calls=%d", err, calls.Load())
	}
}
