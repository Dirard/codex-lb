package application

import "maps"

// WorkerLoad is the admission pressure one runtime process holds.
type WorkerLoad struct {
	Active   int                    `json:"active"`
	Queued   int                    `json:"queued"`
	Accounts map[string]AccountLoad `json:"accounts,omitempty"`
}

// AccountLoad is one process's pressure on one ChatGPT account: response
// creates still waiting for their first upstream event, open streams, and open
// streams per API key.
type AccountLoad struct {
	Creates int            `json:"creates"`
	Streams int            `json:"streams"`
	Keys    map[string]int `json:"keys,omitempty"`
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
	return sum
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
// channel holds fewer and the next call tops them up; released tokens hand
// their slot to a waiting local admission at once. Per account, peer creates
// and streams count against the account limits and peer key streams against
// API-key fair share; every change wakes waiting account admissions.
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
// method adds or removes peer tokens, under admissionMu; local holders remove
// only tokens they added, so the channels always contain the peer tokens and
// the receives below never block.
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
		clean := AccountLoad{Creates: max(0, load.Creates), Streams: max(0, load.Streams)}
		for key, streams := range load.Keys {
			if key == "" || streams <= 0 {
				continue
			}
			if clean.Keys == nil {
				clean.Keys = make(map[string]int, len(load.Keys))
			}
			clean.Keys[key] = streams
		}
		if clean.Creates == 0 && clean.Streams == 0 && len(clean.Keys) == 0 {
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
		return x.Creates == y.Creates && x.Streams == y.Streams && maps.Equal(x.Keys, y.Keys)
	}) {
		return
	}
	a.peers = peers
	a.notifyLocked()
}

// ownLoad copies this process's account pressure, excluding peers.
func (a *accountAdmission) ownLoad() map[string]AccountLoad {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.accounts) == 0 {
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
	return accounts
}
