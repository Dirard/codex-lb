package application

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"codex-lb/internal/domain"
)

const (
	// voiceAttempts bounds how many accounts one voice request may reach.
	voiceAttempts = 3
	// voiceDefaultCooldown applies when a refusal or warning names no reset.
	voiceDefaultCooldown = 15 * time.Minute
	voiceMaxCooldown     = 24 * time.Hour
)

// voiceBalance is one worker's view of voice traffic per subscription
// account: the calls and sessions it serves and the voice limits the
// subscription reported. It is never persisted; a new worker relearns it.
type voiceBalance struct {
	mu      sync.Mutex
	active  map[string]int
	refused map[string]time.Time
	warned  map[string]time.Time
}

func newVoiceBalance() *voiceBalance {
	return &voiceBalance{active: map[string]int{}, refused: map[string]time.Time{}, warned: map[string]time.Time{}}
}

// begin counts a call or session on its account until the returned release.
func (b *voiceBalance) begin(accountID string) func() {
	b.mu.Lock()
	b.active[accountID]++
	b.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			if b.active[accountID]--; b.active[accountID] <= 0 {
				delete(b.active, accountID)
			}
		})
	}
}

// refuse keeps new voice requests off an account the subscription refused
// for a limit, until the refusal's reset.
func (b *voiceBalance) refuse(accountID string, until time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if until.After(b.refused[accountID]) {
		b.refused[accountID] = until
	}
}

// warn ranks an account last for voice until a reported limit resets.
func (b *voiceBalance) warn(accountID string, until time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.warned[accountID] = until
}

// voiceCooldown bounds a reported reset in seconds to a day, else the
// default.
func voiceCooldown(seconds float64) time.Duration {
	if seconds < 1 || seconds > voiceMaxCooldown.Seconds() {
		return voiceDefaultCooldown
	}
	return time.Duration(seconds * float64(time.Second))
}

// voiceQuotaRefusal reports whether a voice attempt was refused for a limit,
// which means the subscription created nothing, and for how long to keep new
// voice requests off its account.
func voiceQuotaRefusal(err error) (time.Duration, bool) {
	var failure *ProviderFailure
	if !errors.As(err, &failure) || failure.Status != http.StatusTooManyRequests {
		return 0, false
	}
	seconds := 0.0
	for name, values := range failure.LimitHeaders {
		if strings.EqualFold(name, "Retry-After") && len(values) != 0 {
			seconds, _ = strconv.ParseFloat(strings.TrimSpace(values[0]), 64)
		}
	}
	return voiceCooldown(seconds), true
}

// voiceAccountState is what ranks one candidate.
type voiceAccountState struct {
	account   domain.Account
	warned    bool
	exhausted bool
	active    int
	used      float64
}

// voiceAccounts keeps a pinned conversation owner, drops accounts refused for
// voice and ranks the rest by voice warnings, exhausted Codex windows, live
// voice traffic and window use.
func (s *CodexOperations) voiceAccounts(ctx context.Context, keyID string, owner *domain.Continuation) ([]domain.Account, error) {
	if owner != nil {
		account, err := s.accountForOwner(ctx, keyID, owner, nil)
		if err != nil {
			return nil, err
		}
		return []domain.Account{account}, nil
	}
	accounts, err := s.store.EligibleAccounts(ctx, keyID)
	if err != nil {
		return nil, err
	}
	candidates := make([]domain.Account, 0, len(accounts))
	ids := make([]string, 0, len(accounts))
	for _, account := range accounts {
		if account.Kind == domain.AccountChatGPT && account.Status == domain.AccountActive {
			candidates = append(candidates, account)
			ids = append(ids, account.ID)
		}
	}
	if len(candidates) == 0 {
		return nil, domain.ErrNoAccounts
	}
	var telemetry map[string]domain.AccountSelectionState
	if snapshots, ok := s.store.(AccountSelectionSnapshots); ok {
		// Window use only orders accounts; without it voice still balances.
		telemetry, _ = snapshots.AccountSelectionSnapshot(ctx, ids)
	}
	now := s.now()
	states := make([]voiceAccountState, 0, len(candidates))
	var soonest time.Time
	s.voice.mu.Lock()
	for _, account := range candidates {
		if until := s.voice.refused[account.ID]; until.After(now) {
			if soonest.IsZero() || until.Before(soonest) {
				soonest = until
			}
			continue
		}
		state := voiceAccountState{account: account, warned: s.voice.warned[account.ID].After(now), active: s.voice.active[account.ID]}
		for _, quota := range telemetry[account.ID].Quotas {
			if quota.ResetAt != nil && !quota.ResetAt.After(now) {
				continue // A window that already reset says nothing about now.
			}
			state.used = max(state.used, quota.UsedPercent)
			state.exhausted = state.exhausted || quota.UsedPercent >= 100 && quota.ResetAt != nil
		}
		states = append(states, state)
	}
	s.voice.mu.Unlock()
	if len(states) == 0 {
		wait := max(1, int64(soonest.Sub(now).Seconds()+0.999))
		return nil, &ProviderFailure{Code: "voice_limit_reached", Status: http.StatusTooManyRequests, QuotaRefused: true,
			LimitHeaders: map[string][]string{"Retry-After": {strconv.FormatInt(wait, 10)}}}
	}
	sort.SliceStable(states, func(i, j int) bool {
		a, b := states[i], states[j]
		switch {
		case a.warned != b.warned:
			return !a.warned
		case a.exhausted != b.exhausted:
			return !a.exhausted
		case a.active != b.active:
			return a.active < b.active
		case a.used != b.used:
			return a.used < b.used
		default:
			return a.account.ID < b.account.ID
		}
	})
	ranked := make([]domain.Account, len(states))
	for i, state := range states {
		ranked[i] = state.account
	}
	return ranked, nil
}
