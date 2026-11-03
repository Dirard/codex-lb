package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

// voiceStore serves only what voice account ranking reads.
type voiceStore struct {
	CodexOperationStore
	accounts []domain.Account
	states   map[string]domain.AccountSelectionState
}

func (s voiceStore) EligibleAccounts(context.Context, string) ([]domain.Account, error) {
	return s.accounts, nil
}

func (s voiceStore) AccountSelectionSnapshot(context.Context, []string) (map[string]domain.AccountSelectionState, error) {
	return s.states, nil
}

func (s voiceStore) ScopedAccountForOwner(_ context.Context, _ string, accountID string, _ time.Time) (domain.Account, error) {
	for _, account := range s.accounts {
		if account.ID == accountID {
			return account, nil
		}
	}
	return domain.Account{}, domain.ErrNotFound
}

func voiceIDs(accounts []domain.Account) []string {
	ids := make([]string, len(accounts))
	for i, account := range accounts {
		ids[i] = account.ID
	}
	return ids
}

func TestVoiceAccountsRankByWarningsWindowsLoadAndUse(t *testing.T) {
	now := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	later, earlier := now.Add(time.Hour), now.Add(-time.Minute)
	chatgpt := func(id string) domain.Account {
		return domain.Account{ID: id, Kind: domain.AccountChatGPT, Status: domain.AccountActive, Provider: "openai"}
	}
	window := func(used float64, reset time.Time) domain.AccountSelectionState {
		return domain.AccountSelectionState{Quotas: []domain.AccountQuota{{Window: "weekly", UsedPercent: used, ResetAt: &reset}}}
	}
	store := voiceStore{
		accounts: []domain.Account{
			chatgpt("a-warned"), chatgpt("b-exhausted"), chatgpt("c-busy"), chatgpt("d-used"), chatgpt("e-fresh"),
			chatgpt("f-refused"), chatgpt("g-reset"),
			{ID: "h-external", Kind: domain.AccountExternal, Status: domain.AccountActive},
			{ID: "i-limited", Kind: domain.AccountChatGPT, Status: domain.AccountRateLimited},
		},
		states: map[string]domain.AccountSelectionState{
			"b-exhausted": window(100, later),
			"d-used":      window(80, later),
			"e-fresh":     window(10, later),
			"g-reset":     window(100, earlier), // already reset: says nothing now
		},
	}
	ops := &CodexOperations{store: store, now: func() time.Time { return now }, voice: newVoiceBalance()}
	ops.voice.warn("a-warned", later)
	ops.voice.refuse("f-refused", later)
	busy := ops.voice.begin("c-busy")
	ranked, err := ops.voiceAccounts(context.Background(), "key", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"g-reset", "e-fresh", "d-used", "c-busy", "b-exhausted", "a-warned"}
	if got := voiceIDs(ranked); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] ||
		got[3] != want[3] || got[4] != want[4] || got[5] != want[5] {
		t.Fatalf("ranked %v, want %v", got, want)
	}
	busy()
	busy() // A release runs once.
	if ops.voice.active["c-busy"] != 0 {
		t.Fatalf("active count %d after release", ops.voice.active["c-busy"])
	}
	// A pinned owner keeps its account, even one refused for voice.
	owner := &domain.Continuation{AccountID: "f-refused", ProviderID: "openai"}
	if pinned, err := ops.voiceAccounts(context.Background(), "key", owner); err != nil || len(pinned) != 1 || pinned[0].ID != "f-refused" {
		t.Fatalf("pinned %v %v", voiceIDs(pinned), err)
	}
}

func TestVoiceAccountsRefuseWhenEveryAccountIsCoolingDown(t *testing.T) {
	now := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	store := voiceStore{accounts: []domain.Account{
		{ID: "a", Kind: domain.AccountChatGPT, Status: domain.AccountActive},
		{ID: "b", Kind: domain.AccountChatGPT, Status: domain.AccountActive},
	}}
	clock := now
	ops := &CodexOperations{store: store, now: func() time.Time { return clock }, voice: newVoiceBalance()}
	ops.voice.refuse("a", now.Add(10*time.Minute))
	ops.voice.refuse("b", now.Add(90*time.Second+300*time.Millisecond))
	_, err := ops.voiceAccounts(context.Background(), "key", nil)
	var failure *ProviderFailure
	if !errors.As(err, &failure) || failure.Status != 429 || failure.Code != "voice_limit_reached" ||
		len(failure.LimitHeaders["Retry-After"]) != 1 || failure.LimitHeaders["Retry-After"][0] != "91" {
		t.Fatalf("all refused: %#v", err)
	}
	clock = now.Add(2 * time.Minute) // b's cooldown is over
	if ranked, err := ops.voiceAccounts(context.Background(), "key", nil); err != nil || len(ranked) != 1 || ranked[0].ID != "b" {
		t.Fatalf("after b's cooldown: %v %v", voiceIDs(ranked), err)
	}
}

func TestVoiceQuotaRefusalReadsRetryAfter(t *testing.T) {
	for _, tc := range []struct {
		err      error
		cooldown time.Duration
		refused  bool
	}{
		{&ProviderFailure{Status: 429, LimitHeaders: map[string][]string{"Retry-After": {"600"}}}, 10 * time.Minute, true},
		{&ProviderFailure{Status: 429}, voiceDefaultCooldown, true},
		{&ProviderFailure{Status: 429, LimitHeaders: map[string][]string{"Retry-After": {"999999"}}}, voiceDefaultCooldown, true},
		{&ProviderFailure{Status: 502}, 0, false},
		{errors.New("boom"), 0, false},
	} {
		cooldown, refused := voiceQuotaRefusal(tc.err)
		if cooldown != tc.cooldown || refused != tc.refused {
			t.Fatalf("%v: %v %v, want %v %v", tc.err, cooldown, refused, tc.cooldown, tc.refused)
		}
	}
}
