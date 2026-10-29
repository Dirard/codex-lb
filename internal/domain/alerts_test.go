package domain

import (
	"fmt"
	"testing"
	"time"
)

func limitAt(percent float64) *LimitState {
	return &LimitState{Type: LimitTotalTokens, Window: WindowDaily, Percent: percent, ResetAt: overviewNow.Add(time.Hour)}
}

func alertsOf(in AlertInputs, rule AlertRule) []Alert {
	in.Now = overviewNow
	var out []Alert
	for _, alert := range EvaluateAlerts(in) {
		if alert.Rule == rule {
			out = append(out, alert)
		}
	}
	return out
}

func severityOf(alerts []Alert) string {
	if len(alerts) == 0 {
		return "none"
	}
	return string(alerts[0].Severity)
}

func TestKeyLimitAlertBoundaries(t *testing.T) {
	group := "grp"
	for _, test := range []struct {
		percent float64
		want    string
	}{{89.9, "none"}, {90, "warning"}, {99.99, "warning"}, {100, "critical"}, {130, "critical"}} {
		got := alertsOf(AlertInputs{Keys: []AlertKey{{ID: "k", Name: "k", MaxLimit: limitAt(test.percent)}}}, AlertKeyLimit)
		if severityOf(got) != test.want {
			t.Errorf("%v %%: %s, want %s", test.percent, severityOf(got), test.want)
		}
		if len(got) == 1 && (got[0].Action.Kind != "edit_limits" || got[0].Action.Target != "k" || got[0].ID != "key_limit:key:k:daily") {
			t.Errorf("ungrouped action %+v id %s", got[0].Action, got[0].ID)
		}
	}
	grouped := alertsOf(AlertInputs{Keys: []AlertKey{{ID: "k", Name: "k", GroupID: &group, MaxLimit: limitAt(95)}}}, AlertKeyLimit)
	if len(grouped) != 1 || grouped[0].Action.Kind != "open_group_limits" || grouped[0].Action.Target != "grp" {
		t.Fatalf("grouped key opens the group's limits: %+v", grouped)
	}
}

func TestKeyExpiryAlertBoundaries(t *testing.T) {
	for _, test := range []struct {
		left time.Duration
		want string
	}{{-time.Minute, "none"}, {time.Hour, "critical"}, {24 * time.Hour, "critical"}, {24*time.Hour + time.Second, "warning"},
		{7 * 24 * time.Hour, "warning"}, {7*24*time.Hour + time.Second, "none"}} {
		got := alertsOf(AlertInputs{Keys: []AlertKey{{ID: "k", Name: "k", ExpiresAt: at(test.left)}}}, AlertKeyExpiry)
		if severityOf(got) != test.want {
			t.Errorf("expires in %v: %s, want %s", test.left, severityOf(got), test.want)
		}
		if len(got) == 1 && got[0].Action.Kind != "extend_expiry" {
			t.Errorf("action %+v", got[0].Action)
		}
	}
}

func TestErrorSpikeBoundaries(t *testing.T) {
	for _, test := range []struct {
		name           string
		last, trailing ErrorWindow
		want           string
	}{
		{name: "19 errors", last: ErrorWindow{Requests: 50, Errors: 19}, want: "none"},
		{name: "20 errors at 40 %", last: ErrorWindow{Requests: 50, Errors: 20}, want: "critical"},
		{name: "20 % critical", last: ErrorWindow{Requests: 100, Errors: 20}, want: "critical"},
		{name: "baseline floor 1 %", last: ErrorWindow{Requests: 400, Errors: 20}, trailing: ErrorWindow{Requests: 1000, Errors: 0}, want: "warning"},
		{name: "below 3x baseline", last: ErrorWindow{Requests: 400, Errors: 24}, trailing: ErrorWindow{Requests: 1000, Errors: 30}, want: "none"},
		{name: "above 3x baseline", last: ErrorWindow{Requests: 500, Errors: 32}, trailing: ErrorWindow{Requests: 1000, Errors: 20}, want: "warning"},
		{name: "under 5 %", last: ErrorWindow{Requests: 1000, Errors: 49}, want: "none"},
	} {
		got := alertsOf(AlertInputs{ErrorsKnown: true, PoolLastHour: test.last, PoolTrailing: test.trailing}, AlertErrorSpike)
		if severityOf(got) != test.want {
			t.Errorf("%s: %s, want %s", test.name, severityOf(got), test.want)
		}
	}
	unknown := alertsOf(AlertInputs{PoolLastHour: ErrorWindow{Requests: 50, Errors: 50}}, AlertErrorSpike)
	if len(unknown) != 0 {
		t.Fatal("unknown error windows raise nothing")
	}
	for _, test := range []struct {
		window ErrorWindow
		want   string
	}{{ErrorWindow{Requests: 80, Errors: 19}, "none"}, {ErrorWindow{Requests: 80, Errors: 20}, "warning"}, {ErrorWindow{Requests: 81, Errors: 20}, "none"}} {
		got := alertsOf(AlertInputs{Keys: []AlertKey{{ID: "k", Name: "k", LastHour: test.window, LastHourKnown: true}}}, AlertKeyErrorSpike)
		if severityOf(got) != test.want {
			t.Errorf("key %+v: %s, want %s", test.window, severityOf(got), test.want)
		}
	}
}

func TestStaleQuotaBoundaries(t *testing.T) {
	for _, test := range []struct {
		age  *time.Duration
		want string
	}{{durationPointer(10 * time.Minute), "none"}, {durationPointer(10*time.Minute + time.Second), "warning"},
		{durationPointer(60 * time.Minute), "warning"}, {durationPointer(60*time.Minute + time.Second), "critical"}, {nil, "warning"}} {
		var last *time.Time
		if test.age != nil {
			last = at(-*test.age)
		}
		got := alertsOf(AlertInputs{LastQuotaAt: last, PolledChatGPTCount: 1}, AlertQuotaDataStale)
		if severityOf(got) != test.want {
			t.Errorf("age %v: %s, want %s", test.age, severityOf(got), test.want)
		}
	}
	if got := alertsOf(AlertInputs{LastQuotaAt: at(-2 * time.Hour)}, AlertQuotaDataStale); len(got) != 0 {
		t.Fatal("no polled ChatGPT accounts, no stale alert")
	}
}

func durationPointer(d time.Duration) *time.Duration { return &d }

func TestRunwayAlertBoundaries(t *testing.T) {
	group := "grp"
	window := func(key string, remaining float64, exhaustion *time.Time) AlertRunwaySubject {
		health := WindowHealth{RemainingPercent: &remaining, ProjectedExhaustionAt: exhaustion, RunsOutBeforeReset: exhaustion != nil}
		return AlertRunwaySubject{ID: &group, Name: "G", Windows: []AlertWindow{{Key: key, Health: health}}}
	}
	for _, test := range []struct {
		name    string
		subject AlertRunwaySubject
		want    string
	}{
		{"lasts until reset", window("5h", 50, nil), "none"},
		{"5h runs out in 2 h", window("5h", 50, at(2*time.Hour)), "warning"},
		{"5h runs out in 1 h", window("5h", 50, at(time.Hour)), "critical"},
		{"weekly runs out in 13 h", window("weekly", 50, at(13*time.Hour)), "warning"},
		{"weekly runs out in 12 h", window("weekly", 50, at(12*time.Hour)), "critical"},
		{"5 % remaining", window("weekly", 5, nil), "critical"},
		{"5.1 % remaining", window("weekly", 5.1, nil), "none"},
	} {
		got := alertsOf(AlertInputs{Runways: []AlertRunwaySubject{test.subject}}, AlertQuotaRunway)
		if severityOf(got) != test.want {
			t.Errorf("%s: %s, want %s", test.name, severityOf(got), test.want)
		}
		if len(got) == 1 && (got[0].Action.Kind != "open_group" || got[0].ID != "quota_runway:group:grp:"+test.subject.Windows[0].Key) {
			t.Errorf("%s: %+v %s", test.name, got[0].Action, got[0].ID)
		}
	}
	remaining := 3.0
	pool := alertsOf(AlertInputs{Runways: []AlertRunwaySubject{{Name: "pool", Windows: []AlertWindow{{Key: "5h",
		Health: WindowHealth{RemainingPercent: &remaining}}}}}}, AlertQuotaRunway)
	if len(pool) != 1 || pool[0].Action.Kind != "open_accounts" || pool[0].Subject.Type != "pool" {
		t.Fatalf("pool runway opens Accounts: %+v", pool)
	}
}

func TestAlertsCollapseAndOrder(t *testing.T) {
	var keys []AlertKey
	for i := range 6 {
		keys = append(keys, AlertKey{ID: fmt.Sprintf("k%d", i), Name: fmt.Sprintf("key %d", i), MaxLimit: limitAt(90 + float64(i))})
	}
	keys = append(keys, AlertKey{ID: "blocked", Name: "blocked", MaxLimit: limitAt(120)},
		AlertKey{ID: "expiring", Name: "expiring", ExpiresAt: at(48 * time.Hour)})
	alerts := EvaluateAlerts(AlertInputs{Now: overviewNow, Keys: keys,
		ReauthAccounts: []AlertAccount{{ID: "acct", Label: "acct", GroupIDs: []string{"g1", "g2"}}},
		ErrorsKnown:    true, PoolLastHour: ErrorWindow{Requests: 100, Errors: 30}})
	var got []string
	for _, alert := range alerts {
		got = append(got, fmt.Sprintf("%s/%s/%d", alert.Rule, alert.Severity, alert.Count))
	}
	want := []string{"account_reauth/critical/1", "key_limit/critical/1", "error_spike/critical/1",
		"key_limit/warning/6", "key_expiry/warning/1"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("alerts %v, want %v", got, want)
	}
	summary := alerts[3]
	if summary.Action.Kind != "open_list" || summary.Action.Target != "/keys?near_limit=1" || *summary.Value != 95 || summary.ID != "key_limit:summary:warning" {
		t.Fatalf("summary %+v", summary)
	}
	// Five subjects stay individual, ordered by value.
	five := EvaluateAlerts(AlertInputs{Now: overviewNow, Keys: keys[:5]})
	if len(five) != 5 || *five[0].Value != 94 || *five[4].Value != 90 {
		t.Fatalf("five alerts must stay individual, highest first: %+v", five)
	}
}
