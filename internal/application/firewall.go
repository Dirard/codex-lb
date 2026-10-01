package application

import (
	"context"
	"net/netip"
	"sort"
	"sync"
	"time"

	"codex-lb/internal/domain"
)

type FirewallRepository interface {
	ListFirewallEntries(context.Context) ([]domain.FirewallEntry, error)
	AddFirewallEntry(context.Context, domain.FirewallEntry) error
	DeleteFirewallEntry(context.Context, string) error
}

type Firewall struct {
	repo    FirewallRepository
	mu      sync.RWMutex
	entries map[netip.Addr]domain.FirewallEntry
}

func NewFirewall(ctx context.Context, repo FirewallRepository) (*Firewall, error) {
	entries, err := repo.ListFirewallEntries(ctx)
	if err != nil {
		return nil, err
	}
	f := &Firewall{repo: repo, entries: make(map[netip.Addr]domain.FirewallEntry, len(entries))}
	for _, entry := range entries {
		ip, err := firewallIP(entry.IPAddress)
		if err != nil {
			return nil, err
		}
		f.entries[ip] = entry
	}
	return f, nil
}

func firewallIP(raw string) (netip.Addr, error) {
	ip, err := netip.ParseAddr(raw)
	if err != nil || ip.Zone() != "" {
		return netip.Addr{}, domain.ErrInvalid
	}
	return ip.Unmap(), nil
}

func (f *Firewall) Allowed(ip netip.Addr) bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	_, found := f.entries[ip.Unmap()]
	return len(f.entries) == 0 || ip.IsValid() && found
}

func (f *Firewall) Entries() []domain.FirewallEntry {
	f.mu.RLock()
	defer f.mu.RUnlock()
	entries := make([]domain.FirewallEntry, 0, len(f.entries))
	for _, entry := range f.entries {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].CreatedAt.Equal(entries[j].CreatedAt) {
			return entries[i].IPAddress < entries[j].IPAddress
		}
		return entries[i].CreatedAt.Before(entries[j].CreatedAt)
	})
	return entries
}

func (f *Firewall) Add(ctx context.Context, raw string) (domain.FirewallEntry, error) {
	ip, err := firewallIP(raw)
	if err != nil {
		return domain.FirewallEntry{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, found := f.entries[ip]; found {
		return domain.FirewallEntry{}, domain.ErrConflict
	}
	if len(f.entries) >= 4096 {
		return domain.FirewallEntry{}, domain.ErrInvalid
	}
	entry := domain.FirewallEntry{IPAddress: ip.String(), CreatedAt: time.Now().UTC()}
	if err := f.repo.AddFirewallEntry(ctx, entry); err != nil {
		return domain.FirewallEntry{}, err
	}
	f.entries[ip] = entry
	return entry, nil
}

func (f *Firewall) Delete(ctx context.Context, raw string) error {
	ip, err := firewallIP(raw)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, found := f.entries[ip]; !found {
		return domain.ErrNotFound
	}
	if err := f.repo.DeleteFirewallEntry(ctx, ip.String()); err != nil {
		return err
	}
	delete(f.entries, ip)
	return nil
}
