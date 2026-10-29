package domain

import (
	"fmt"
	"testing"
	"time"
)

func attentionKey(id string, spend float64, in AttentionSignals) AttentionKey {
	reasons := KeyAttentionReasons(in, overviewNow)
	key := AttentionKey{ID: id, Name: id, Reasons: reasons, SpendTodayUSD: spend}
	if len(reasons) > 0 {
		key.PrimaryAction = PrimaryAttentionAction(reasons[0].Reason, false)
	}
	return key
}

func TestAttentionReasons(t *testing.T) {
	flat := []float64{1, 1, 1, 1, 1, 1, 1}
	for _, test := range []struct {
		name string
		in   AttentionSignals
		want string
	}{
		{"nothing", AttentionSignals{SpendToday: 5, PreviousDays: flat}, "[]"},
		{"blocked", AttentionSignals{MaxLimit: limitAt(100)}, "[limit_blocked/critical]"},
		{"near", AttentionSignals{MaxLimit: limitAt(90)}, "[limit_near/warning]"},
		{"error spike", AttentionSignals{LastHour: ErrorWindow{Requests: 40, Errors: 20}, LastHourKnown: true}, "[error_spike/warning]"},
		{"expiring critical first", AttentionSignals{MaxLimit: limitAt(95), ExpiresAt: at(time.Hour)}, "[expiring/critical limit_near/warning]"},
		{"spend spike at the floor", AttentionSignals{SpendToday: 10, PreviousDays: flat}, "[spend_spike/warning]"},
		{"spend spike below the floor", AttentionSignals{SpendToday: 9.99, PreviousDays: []float64{0, 0, 0, 0, 0, 0, 0}}, "[]"},
		{"no spike at 2x minus", AttentionSignals{SpendToday: 13.9, PreviousDays: []float64{7, 7, 7, 7, 7, 7, 7}}, "[]"},
		{"spike at 2x", AttentionSignals{SpendToday: 14, PreviousDays: []float64{7, 7, 7, 7, 7, 7, 7}}, "[spend_spike/warning]"},
		{"unknown history has no spike", AttentionSignals{SpendToday: 100}, "[]"},
	} {
		var got []string
		for _, reason := range KeyAttentionReasons(test.in, overviewNow) {
			got = append(got, fmt.Sprintf("%s/%s", reason.Reason, reason.Severity))
		}
		if fmt.Sprintf("%v", got) != test.want && !(test.want == "[]" && len(got) == 0) {
			t.Errorf("%s: %v, want %s", test.name, got, test.want)
		}
	}
}

func TestRankAttentionKeys(t *testing.T) {
	flat := []float64{1, 1, 1, 1, 1, 1, 1}
	keys := []AttentionKey{
		attentionKey("quiet", 100, AttentionSignals{SpendToday: 1, PreviousDays: flat}),
		attentionKey("spike", 50, AttentionSignals{SpendToday: 50, PreviousDays: flat}),
		attentionKey("near-cheap", 1, AttentionSignals{MaxLimit: limitAt(92)}),
		attentionKey("near-rich", 9, AttentionSignals{MaxLimit: limitAt(92)}),
		attentionKey("near-high", 0, AttentionSignals{MaxLimit: limitAt(97)}),
		attentionKey("blocked", 0, AttentionSignals{MaxLimit: limitAt(100)}),
		attentionKey("expiring-soon", 0, AttentionSignals{ExpiresAt: at(2 * time.Hour)}),
		attentionKey("expiring-sooner", 0, AttentionSignals{ExpiresAt: at(time.Hour)}),
		attentionKey("expiring-week", 0, AttentionSignals{ExpiresAt: at(72 * time.Hour)}),
	}
	top, total := RankAttentionKeys(keys, 6)
	if total != 8 {
		t.Fatalf("total %d, want 8 keys with a reason", total)
	}
	var got []string
	for _, key := range top {
		got = append(got, key.ID)
	}
	want := []string{"blocked", "expiring-sooner", "expiring-soon", "near-high", "near-rich", "near-cheap"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("ranking %v, want %v", got, want)
	}
	if top[0].PrimaryAction != "edit_limits" || top[1].PrimaryAction != "extend_expiry" {
		t.Fatalf("primary actions %s %s", top[0].PrimaryAction, top[1].PrimaryAction)
	}
	if PrimaryAttentionAction(ReasonLimitNear, true) != "open_group_limits" || PrimaryAttentionAction(ReasonSpendSpike, false) != "open_key" ||
		PrimaryAttentionAction(ReasonErrorSpike, false) != "open_key_errors" {
		t.Fatal("primary action table")
	}
	empty, none := RankAttentionKeys(nil, 10)
	if none != 0 || empty == nil || len(empty) != 0 {
		t.Fatal("empty ranking is an empty list")
	}
}
