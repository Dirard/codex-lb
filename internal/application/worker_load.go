package application

import (
	"maps"
	"slices"
)

// WorkerLoad is the admission pressure one runtime process holds.
type WorkerLoad struct {
	Active   int                    `json:"active"`
	Queued   int                    `json:"queued"`
	Accounts map[string]AccountLoad `json:"accounts,omitempty"`
}

// AccountLoad is one process's pressure on one ChatGPT account: response
// creates still waiting for their first upstream event, open streams, open
// streams per API key, and the API keys whose admissions wait for the account
// (sorted and distinct).
type AccountLoad struct {
	Creates int            `json:"creates"`
	Streams int            `json:"streams"`
	Keys    map[string]int `json:"keys,omitempty"`
	Waiting []string       `json:"waiting,omitempty"`
}

// Add returns the deep sum of both loads. Neither input is mutated and the
// result shares no map with them.
func (l WorkerLoad) Add(other WorkerLoad) WorkerLoad {
	sum := WorkerLoad{Active: l.Active + other.Active, Queued: l.Queued + other.Queued}
	if len(l.Accounts)+len(other.Accounts) != 0 {
		sum.Accounts = make(map[string]AccountLoad, max(len(l.Accounts), len(other.Accounts)))
		for _, accounts := range []map[string]AccountLoad{l.Accounts, other.Accounts} {
			for id, load := range accounts {
				sum.Accounts[id] = sum.Accounts[id].add(load)
			}
		}
	}
	return sum
}

func (l AccountLoad) add(other AccountLoad) AccountLoad {
	sum := AccountLoad{Creates: l.Creates + other.Creates, Streams: l.Streams + other.Streams}
	if len(l.Keys)+len(other.Keys) != 0 {
		sum.Keys = make(map[string]int, max(len(l.Keys), len(other.Keys)))
		for _, keys := range []map[string]int{l.Keys, other.Keys} {
			for key, streams := range keys {
				sum.Keys[key] += streams
			}
		}
	}
	sum.Waiting = distinctKeys(l.Waiting, other.Waiting)
	return sum
}

// distinctKeys returns the sorted distinct non-empty keys of all lists in a
// new slice, or nil when there are none.
func distinctKeys(lists ...[]string) []string {
	var keys []string
	for _, list := range lists {
		for _, key := range list {
			if key != "" {
				keys = append(keys, key)
			}
		}
	}
	slices.Sort(keys)
	return slices.Compact(keys)
}

// Load reports the work this process admitted itself. Peer load set through
// SetPeerLoad is never included, so a launcher can sum loads without echoes.
func (p *Proxy) Load() WorkerLoad {
	active, queued := p.AdmissionUsage()
	return WorkerLoad{Active: active, Queued: queued, Accounts: p.accountAdmission.ownLoad()}
}

// SetPeerLoad replaces the load the other live processes of this installation
// hold (an empty value clears it), so local admission over the union stays
// within the host's capacity plan.
//
// Globally, peer slots are held as tokens in the local stream and admission
// channels: peer active work occupies stream slots, peer active plus queued
// work occupies admission slots. Tokens are taken without blocking, so a full
// channel holds fewer; a local token released while fewer are held than wanted
// becomes a peer token, and the next call tops up the rest. Released peer
// tokens hand their slot to a waiting local admission at once. Per account,
// peer creates and streams count against the account limits; peer key streams
// count against API-key fair share, where keys waiting on a peer are active
// keys too. Every change wakes waiting account admissions.
func (p *Proxy) SetPeerLoad(peer WorkerLoad) {
	streams := min(max(0, peer.Active), cap(p.streams))
	admitted := cap(p.admitted)
	admitted = min(min(max(0, peer.Active), admitted)+min(max(0, peer.Queued), admitted), admitted)
	p.admissionMu.Lock()
	p.peerStreamsWanted, p.peerAdmittedWanted = streams, admitted
	p.holdPeerSlotsLocked()
	p.admissionMu.Unlock()
	p.accountAdmission.setPeer(peer.Accounts)
}

// holdPeerSlotsLocked moves the peer tokens toward the wanted counts. Only this
// method removes peer tokens and, besides a local release turning its own
// token into one (releaseSlotsLocked), adds them, always under admissionMu;
// local holders remove only tokens they added, so the channels always contain
// the peer tokens and the receives below never block.
func (p *Proxy) holdPeerSlotsLocked() {
	for p.peerStreams > p.peerStreamsWanted {
		<-p.streams
		p.peerStreams--
	}
	for p.peerAdmitted > p.peerAdmittedWanted {
		<-p.admitted
		p.peerAdmitted--
	}
	for p.peerAdmitted < p.peerAdmittedWanted && trySignal(p.admitted) {
		p.peerAdmitted++
	}
	for p.peerStreams < p.peerStreamsWanted && trySignal(p.streams) {
		p.peerStreams++
	}
}

func trySignal(slots chan struct{}) bool {
	select {
	case slots <- struct{}{}:
		return true
	default:
		return false
	}
}

// setPeer replaces the peers' account pressure with a sanitized copy and
// wakes waiting admissions when it changed.
func (a *accountAdmission) setPeer(accounts map[string]AccountLoad) {
	var peers map[string]AccountLoad
	for id, load := range accounts {
		clean := AccountLoad{Creates: max(0, load.Creates), Streams: max(0, load.Streams), Waiting: distinctKeys(load.Waiting)}
		for key, streams := range load.Keys {
			if key == "" || streams <= 0 {
				continue
			}
			if clean.Keys == nil {
				clean.Keys = make(map[string]int, len(load.Keys))
			}
			clean.Keys[key] = streams
		}
		if clean.Creates == 0 && clean.Streams == 0 && len(clean.Keys) == 0 && len(clean.Waiting) == 0 {
			continue
		}
		if peers == nil {
			peers = make(map[string]AccountLoad, len(accounts))
		}
		peers[id] = clean
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if maps.EqualFunc(a.peers, peers, func(x, y AccountLoad) bool {
		return x.Creates == y.Creates && x.Streams == y.Streams && maps.Equal(x.Keys, y.Keys) && slices.Equal(x.Waiting, y.Waiting)
	}) {
		return
	}
	a.peers = peers
	a.notifyLocked()
}

// ownLoad copies this process's account pressure and the keys of its waiting
// account admissions, excluding peers.
func (a *accountAdmission) ownLoad() map[string]AccountLoad {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.accounts) == 0 && len(a.waiters) == 0 {
		return nil
	}
	accounts := make(map[string]AccountLoad, len(a.accounts))
	for id, state := range a.accounts {
		load := AccountLoad{Creates: state.creates, Streams: state.streams}
		if len(state.keys) != 0 {
			load.Keys = maps.Clone(state.keys)
		}
		accounts[id] = load
	}
	// Like local fair share, waiters with the local proxy key are no key.
	for _, waiter := range a.waiters {
		if waiter.key == "" {
			continue
		}
		for id := range waiter.accounts {
			load := accounts[id]
			load.Waiting = append(load.Waiting, waiter.key)
			accounts[id] = load
		}
	}
	for id, load := range accounts {
		if len(load.Waiting) > 1 {
			load.Waiting = distinctKeys(load.Waiting)
			accounts[id] = load
		}
	}
	if len(accounts) == 0 {
		return nil
	}
	return accounts
}
