package application

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"codex-lb/internal/domain"
)

type accountPressure struct {
	creates, streams int
	keys             map[string]int
}

type accountWaiter struct {
	key      string
	accounts map[string]bool
}

type accountAdmission struct {
	mu       sync.Mutex
	accounts map[string]*accountPressure
	waiters  map[uint64]accountWaiter
	next     uint64
	changed  chan struct{}
}

func newAccountAdmission() *accountAdmission {
	return &accountAdmission{accounts: make(map[string]*accountPressure), waiters: make(map[uint64]accountWaiter), changed: make(chan struct{})}
}

type accountLease struct {
	admission  *accountAdmission
	accountID  string
	keyID      string
	stream     bool
	createOnce sync.Once
	streamOnce sync.Once
}

func (lease *accountLease) releaseCreate() {
	if lease == nil {
		return
	}
	lease.createOnce.Do(func() {
		a := lease.admission
		a.mu.Lock()
		defer a.mu.Unlock()
		state := a.accounts[lease.accountID]
		if state != nil {
			state.creates--
			a.pruneLocked(lease.accountID)
			a.notifyLocked()
		}
	})
}

func (lease *accountLease) release() {
	if lease == nil {
		return
	}
	lease.releaseCreate()
	lease.streamOnce.Do(func() {
		if !lease.stream {
			return
		}
		a := lease.admission
		a.mu.Lock()
		defer a.mu.Unlock()
		state := a.accounts[lease.accountID]
		if state == nil {
			return
		}
		state.streams--
		if lease.keyID != "" {
			state.keys[lease.keyID]--
			if state.keys[lease.keyID] == 0 {
				delete(state.keys, lease.keyID)
			}
		}
		a.pruneLocked(lease.accountID)
		a.notifyLocked()
	})
}

func (a *accountAdmission) pruneLocked(accountID string) {
	state := a.accounts[accountID]
	if state != nil && state.creates == 0 && state.streams == 0 {
		delete(a.accounts, accountID)
	}
}

func (a *accountAdmission) notifyLocked() {
	close(a.changed)
	a.changed = make(chan struct{})
}

func (a *accountAdmission) removeWaiter(id uint64) {
	if id == 0 {
		return
	}
	a.mu.Lock()
	if _, ok := a.waiters[id]; ok {
		delete(a.waiters, id)
		a.notifyLocked()
	}
	a.mu.Unlock()
}

func (a *accountAdmission) waitLocked(id *uint64, key string, choices []accountCandidate) <-chan struct{} {
	if *id == 0 {
		a.next++
		*id = a.next
	}
	accounts := make(map[string]bool, len(choices))
	for _, choice := range choices {
		if choice.account.Kind == domain.AccountChatGPT {
			accounts[choice.account.ID] = true
		}
	}
	a.waiters[*id] = accountWaiter{key: key, accounts: accounts}
	return a.changed
}

func (a *accountAdmission) fairShareLocked(choices []accountCandidate, settings domain.RuntimeSettings, key string) (int, int, int, int, int, bool) {
	if key == "" || settings.ProxyApiKeyFairShareCongestionThresholdPct == 0 || settings.ProxyAccountStreamLimit <= 0 {
		return 0, 0, 0, 0, 0, false
	}
	pool := make(map[string]bool, len(choices))
	for _, choice := range choices {
		if choice.account.Kind == domain.AccountChatGPT {
			pool[choice.account.ID] = true
		}
	}
	if len(pool) == 0 {
		return 0, 0, 0, 0, 0, false
	}
	perAccount := max(1, settings.ProxyAccountStreamLimit-settings.ProxyAccountStreamRecoveryReserve)
	capacity := math.MaxInt / 100
	if perAccount <= capacity/len(pool) {
		capacity = perAccount * len(pool)
	}
	active := map[string]bool{key: true}
	inflight, requester := 0, 0
	for accountID := range pool {
		if state := a.accounts[accountID]; state != nil {
			inflight += state.streams
			requester += state.keys[key]
			for activeKey := range state.keys {
				active[activeKey] = true
			}
		}
	}
	for _, waiting := range a.waiters {
		if waiting.key == "" {
			continue
		}
		for accountID := range waiting.accounts {
			if pool[accountID] {
				active[waiting.key] = true
				break
			}
		}
	}
	share := max(2, capacity/len(active))
	congested := inflight*100 >= capacity*settings.ProxyApiKeyFairShareCongestionThresholdPct
	return requester, share, inflight, capacity, len(active), congested && requester+1 > share
}

func (p *Proxy) admittedAccount(ctx context.Context, choices []accountCandidate, settings domain.RuntimeSettings, keyID string, confirmedOwner, streamWork bool, preferredAccountID ...string) (accountCandidate, *accountLease, error) {
	if len(choices) == 0 {
		return accountCandidate{}, nil, domain.ErrNoAccounts
	}
	key := keyID
	if key == domain.LocalProxyKeyID {
		key = ""
	}
	deadline := time.NewTimer(p.config.QueueTimeout)
	defer deadline.Stop()
	var waiterID uint64
	defer func() { p.accountAdmission.removeWaiter(waiterID) }()
	for {
		a := p.accountAdmission
		a.mu.Lock()
		requester, share, inflight, capacity, activeKeys, fairDenied := 0, 0, 0, 0, 0, false
		if streamWork && !confirmedOwner {
			requester, share, inflight, capacity, activeKeys, fairDenied = a.fairShareLocked(choices, settings, key)
		}
		available := make([]accountCandidate, 0, len(choices))
		reason := "account_stream_cap"
		for _, choice := range choices {
			if choice.account.Kind != domain.AccountChatGPT {
				available = append(available, choice)
				continue
			}
			state := a.accounts[choice.account.ID]
			if state == nil {
				state = &accountPressure{}
			}
			if settings.ProxyAccountResponseCreateLimit > 0 && state.creates >= settings.ProxyAccountResponseCreateLimit {
				reason = "account_response_create_cap"
				continue
			}
			streamLimit := settings.ProxyAccountStreamLimit
			if streamWork && streamLimit > 0 && !confirmedOwner {
				streamLimit = max(1, streamLimit-settings.ProxyAccountStreamRecoveryReserve)
			}
			if streamWork && streamLimit > 0 && state.streams >= streamLimit {
				reason = "account_stream_cap"
				continue
			}
			if fairDenied {
				reason = "api_key_stream_fair_share"
				continue
			}
			available = append(available, choice)
		}
		if len(available) != 0 {
			selected, err := p.chooseAccount(available, settings, preferredAccountID...)
			if err != nil {
				a.mu.Unlock()
				return accountCandidate{}, nil, err
			}
			if waiterID != 0 {
				delete(a.waiters, waiterID)
				waiterID = 0
			}
			var lease *accountLease
			if selected.account.Kind == domain.AccountChatGPT {
				state := a.accounts[selected.account.ID]
				if state == nil {
					state = &accountPressure{}
					a.accounts[selected.account.ID] = state
				}
				state.creates++
				if streamWork {
					state.streams++
					if key != "" {
						if state.keys == nil {
							state.keys = make(map[string]int)
						}
						state.keys[key]++
					}
				}
				lease = &accountLease{admission: a, accountID: selected.account.ID, keyID: key, stream: streamWork}
			}
			a.notifyLocked()
			a.mu.Unlock()
			return selected, lease, nil
		}
		changed := a.waitLocked(&waiterID, key, choices)
		a.mu.Unlock()
		select {
		case <-ctx.Done():
			return accountCandidate{}, nil, ctx.Err()
		case <-deadline.C:
			message := "Account capacity is exhausted"
			if reason == "api_key_stream_fair_share" {
				message = fmt.Sprintf("API key holds %d streams of fair share %d (%d/%d pool streams in flight across %d active keys)", requester, share, inflight, capacity, activeKeys)
			}
			return accountCandidate{}, nil, &ProxyError{Code: reason, Status: 429, Message: message}
		case <-changed:
		}
	}
}
