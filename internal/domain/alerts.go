package domain

import (
	"fmt"
	"sort"
	"time"
)

// Alert thresholds of the overview (dashboard redesign use case 2.2, owner
// decision Q1: fixed constants, not operator settings).
const (
	KeyLimitWarningPercent  = 90.0
	KeyLimitCriticalPercent = 100.0

	RunwayCriticalPrimary          = time.Hour
	RunwayCriticalSecondary        = 12 * time.Hour
	RunwayCriticalRemainingPercent = 5.0

	ErrorSpikeMinErrors       = 20
	ErrorSpikeWarningRate     = 0.05
	ErrorSpikeCriticalRate    = 0.20
	ErrorSpikeBaselineFactor  = 3.0
	ErrorSpikeBaselineFloor   = 0.01
	KeyErrorSpikeMinErrors    = 20
	KeyErrorSpikeRate         = 0.25
	KeyExpiryWarning          = 7 * 24 * time.Hour
	KeyExpiryCritical         = 24 * time.Hour
	QuotaStaleWarning         = 10 * time.Minute
	QuotaStaleCritical        = 60 * time.Minute
	AlertCollapseAbove        = 5
	KeyExpiryExtension        = 30 * 24 * time.Hour
	SpendSpikeFactor          = 2.0
	SpendSpikeMinUSD          = 10.0
	AttentionKeysLimit        = 10
	ProblemAccountsLimit      = 10
	GroupTopKeysLimit         = 5
	OverviewTrendDays         = 7
	OverviewSpendSpikeHistory = 7
)

type AlertRule string

const (
	AlertAccountReauth  AlertRule = "account_reauth"
	AlertQuotaRunway    AlertRule = "quota_runway"
	AlertKeyLimit       AlertRule = "key_limit"
	AlertErrorSpike     AlertRule = "error_spike"
	AlertKeyErrorSpike  AlertRule = "key_error_spike"
	AlertKeyExpiry      AlertRule = "key_expiry"
	AlertQuotaDataStale AlertRule = "quota_data_stale"
)

// alertRuleOrder is the order of the alert-rules table.
var alertRuleOrder = map[AlertRule]int{AlertAccountReauth: 0, AlertQuotaRunway: 1, AlertKeyLimit: 2,
	AlertErrorSpike: 3, AlertKeyErrorSpike: 4, AlertKeyExpiry: 5, AlertQuotaDataStale: 6}

type Severity string

const (
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

func severityRank(s Severity) int {
	if s == SeverityCritical {
		return 0
	}
	return 1
}

type AlertSubject struct {
	Type string  `json:"type"` // pool|group|account|key
	ID   *string `json:"id"`
	Name string  `json:"name"`
}

type AlertAction struct {
	// Kind is reauth, open_group, open_accounts, edit_limits,
	// open_group_limits, open_requests, open_key_errors, extend_expiry or
	// open_list; Target is an entity id or a route.
	Kind   string `json:"kind"`
	Target string `json:"target"`
}

type Alert struct {
	ID       string       `json:"id"`
	Rule     AlertRule    `json:"rule"`
	Severity Severity     `json:"severity"`
	Subject  AlertSubject `json:"subject"`
	Window   *string      `json:"window"`
	// Value is a percent: remaining, limit fill or error rate.
	Value *float64   `json:"value"`
	At    *time.Time `json:"at"`
	Count int        `json:"count"`
	// Group is the group the alert concerns, for group attention; never sent.
	Group *string `json:"-"`
	// Pool marks alerts that concern every account, which count for the
	// ungrouped row's attention.
	Pool   bool        `json:"-"`
	Action AlertAction `json:"action"`
}

// AlertWindow is one quota window of a runway subject.
type AlertWindow struct {
	Key    string // 5h or weekly
	Health WindowHealth
}

// AlertRunwaySubject is the pool (ID nil) or one group.
type AlertRunwaySubject struct {
	ID      *string
	Name    string
	Windows []AlertWindow
}

// AlertAccount is an account that needs re-login.
type AlertAccount struct {
	ID, Label string
	GroupIDs  []string
}

// AlertKey is one active, non-deleted, non-internal key.
type AlertKey struct {
	ID, Name      string
	GroupID       *string
	MaxLimit      *LimitState
	ExpiresAt     *time.Time
	LastHour      ErrorWindow
	LastHourKnown bool
}

// AlertInputs are the overview values the rules read.
type AlertInputs struct {
	Now                time.Time
	Runways            []AlertRunwaySubject
	ReauthAccounts     []AlertAccount
	Keys               []AlertKey
	PoolLastHour       ErrorWindow
	PoolTrailing       ErrorWindow
	ErrorsKnown        bool
	LastQuotaAt        *time.Time
	PolledChatGPTCount int
}

// ErrorWindow counts reported requests and errors of a window; errors
// exclude cancelled requests and the client_disconnected code.
type ErrorWindow struct {
	Requests int64 `json:"requests"`
	Errors   int64 `json:"errors"`
}

// Rate is errors over requests, nil without requests.
func (w ErrorWindow) Rate() *float64 {
	if w.Requests <= 0 {
		return nil
	}
	return floatPointer(float64(w.Errors) / float64(w.Requests))
}

// EvaluateAlerts is the workflow "evaluate each rule with its fixed
// thresholds --> collapse more than five subjects of one rule and severity
// --> sort by severity then rule order then value" of use case 2.2. Nothing
// is stored; the result is recomputed on every overview read.
func EvaluateAlerts(in AlertInputs) []Alert {
	raw := RawAlerts(in)
	return CollapseAlerts(raw)
}

// RawAlerts evaluates every rule without collapsing, for group attention.
func RawAlerts(in AlertInputs) []Alert {
	var alerts []Alert
	for _, account := range in.ReauthAccounts {
		id := account.ID
		alert := Alert{Rule: AlertAccountReauth, Severity: SeverityCritical,
			Subject: AlertSubject{Type: "account", ID: &id, Name: account.Label}, Count: 1,
			Action: AlertAction{Kind: "reauth", Target: account.ID}}
		alert.ID = alertID(alert, "")
		// An account belongs to each of its groups and to the pool.
		alert.Pool = true
		for _, group := range account.GroupIDs {
			copyAlert := alert
			copyAlert.Group = stringPointer(group)
			alerts = append(alerts, copyAlert)
		}
		if len(account.GroupIDs) == 0 {
			alerts = append(alerts, alert)
		}
	}
	for _, subject := range in.Runways {
		for _, window := range subject.Windows {
			if alert, ok := runwayAlert(subject, window, in.Now); ok {
				alerts = append(alerts, alert)
			}
		}
	}
	for _, key := range in.Keys {
		alerts = append(alerts, keyAlerts(key, in.Now)...)
	}
	if in.ErrorsKnown {
		if alert, ok := errorSpikeAlert(in.PoolLastHour, in.PoolTrailing); ok {
			alerts = append(alerts, alert)
		}
	}
	if in.PolledChatGPTCount > 0 {
		if alert, ok := staleQuotaAlert(in.LastQuotaAt, in.Now); ok {
			alerts = append(alerts, alert)
		}
	}
	return alerts
}

func runwayAlert(subject AlertRunwaySubject, window AlertWindow, now time.Time) (Alert, bool) {
	health := window.Health
	critical := health.RemainingPercent != nil && *health.RemainingPercent <= RunwayCriticalRemainingPercent
	within := RunwayCriticalPrimary
	if window.Key != "5h" {
		within = RunwayCriticalSecondary
	}
	if health.RunsOutBeforeReset && health.ProjectedExhaustionAt != nil && !health.ProjectedExhaustionAt.After(now.Add(within)) {
		critical = true
	}
	if !critical && !health.RunsOutBeforeReset {
		return Alert{}, false
	}
	severity := SeverityWarning
	if critical {
		severity = SeverityCritical
	}
	windowKey := window.Key
	alert := Alert{Rule: AlertQuotaRunway, Severity: severity, Window: &windowKey,
		Value: health.RemainingPercent, At: health.ProjectedExhaustionAt, Count: 1}
	if subject.ID == nil {
		alert.Subject = AlertSubject{Type: "pool", Name: subject.Name}
		alert.Action = AlertAction{Kind: "open_accounts", Target: "/accounts"}
		alert.Pool = true
	} else {
		id := *subject.ID
		alert.Subject = AlertSubject{Type: "group", ID: &id, Name: subject.Name}
		alert.Action = AlertAction{Kind: "open_group", Target: id}
		alert.Group = &id
	}
	alert.ID = alertID(alert, windowKey)
	return alert, true
}

func keyAlerts(key AlertKey, now time.Time) []Alert {
	var alerts []Alert
	id := key.ID
	subject := AlertSubject{Type: "key", ID: &id, Name: key.Name}
	if key.MaxLimit != nil && key.MaxLimit.Percent >= KeyLimitWarningPercent {
		severity := SeverityWarning
		if key.MaxLimit.Percent >= KeyLimitCriticalPercent {
			severity = SeverityCritical
		}
		action := AlertAction{Kind: "edit_limits", Target: key.ID}
		if key.GroupID != nil {
			action = AlertAction{Kind: "open_group_limits", Target: *key.GroupID}
		}
		reset := key.MaxLimit.ResetAt
		alert := Alert{Rule: AlertKeyLimit, Severity: severity, Subject: subject,
			Value: floatPointer(key.MaxLimit.Percent), At: &reset, Count: 1, Group: key.GroupID, Action: action}
		alert.ID = alertID(alert, string(key.MaxLimit.Window))
		alerts = append(alerts, alert)
	}
	if key.LastHourKnown && keyErrorSpike(key.LastHour) {
		alert := Alert{Rule: AlertKeyErrorSpike, Severity: SeverityWarning, Subject: subject,
			Value: percentPointer(key.LastHour.Rate()), Count: 1, Group: key.GroupID,
			Action: AlertAction{Kind: "open_key_errors", Target: key.ID}}
		alert.ID = alertID(alert, "1h")
		alerts = append(alerts, alert)
	}
	if severity, ok := expirySeverity(key.ExpiresAt, now); ok {
		expires := *key.ExpiresAt
		alert := Alert{Rule: AlertKeyExpiry, Severity: severity, Subject: subject, At: &expires, Count: 1,
			Group: key.GroupID, Action: AlertAction{Kind: "extend_expiry", Target: key.ID}}
		alert.ID = alertID(alert, "")
		alerts = append(alerts, alert)
	}
	return alerts
}

func keyErrorSpike(window ErrorWindow) bool {
	rate := window.Rate()
	return window.Errors >= KeyErrorSpikeMinErrors && rate != nil && *rate >= KeyErrorSpikeRate
}

// expirySeverity: warning when an active key expires within 7 days, critical
// within 24 hours; an expired key is no longer active.
func expirySeverity(expiresAt *time.Time, now time.Time) (Severity, bool) {
	if expiresAt == nil || !expiresAt.After(now) {
		return "", false
	}
	left := expiresAt.Sub(now)
	switch {
	case left <= KeyExpiryCritical:
		return SeverityCritical, true
	case left <= KeyExpiryWarning:
		return SeverityWarning, true
	}
	return "", false
}

func errorSpikeAlert(lastHour, trailing ErrorWindow) (Alert, bool) {
	rate := lastHour.Rate()
	if rate == nil || lastHour.Errors < ErrorSpikeMinErrors {
		return Alert{}, false
	}
	severity := Severity("")
	baseline := ErrorSpikeBaselineFloor
	if trailingRate := trailing.Rate(); trailingRate != nil {
		baseline = max(baseline, *trailingRate)
	}
	switch {
	case *rate >= ErrorSpikeCriticalRate:
		severity = SeverityCritical
	case *rate >= ErrorSpikeWarningRate && *rate >= ErrorSpikeBaselineFactor*baseline:
		severity = SeverityWarning
	default:
		return Alert{}, false
	}
	window := "1h"
	alert := Alert{Rule: AlertErrorSpike, Severity: severity, Subject: AlertSubject{Type: "pool", Name: "pool"},
		Window: &window, Value: percentPointer(rate), Count: 1, Pool: true,
		Action: AlertAction{Kind: "open_requests", Target: "/requests?status=error&timeframe=1h"}}
	alert.ID = alertID(alert, window)
	return alert, true
}

// staleQuotaAlert: the newest quota observation is older than 10 minutes
// (warning) or 60 minutes (critical) while polled ChatGPT accounts exist; a
// pool that was never observed is a warning.
func staleQuotaAlert(lastQuotaAt *time.Time, now time.Time) (Alert, bool) {
	severity := SeverityWarning
	if lastQuotaAt != nil {
		age := now.Sub(*lastQuotaAt)
		switch {
		case age > QuotaStaleCritical:
			severity = SeverityCritical
		case age > QuotaStaleWarning:
		default:
			return Alert{}, false
		}
	}
	alert := Alert{Rule: AlertQuotaDataStale, Severity: severity, Subject: AlertSubject{Type: "pool", Name: "pool"},
		At: lastQuotaAt, Count: 1, Pool: true, Action: AlertAction{Kind: "open_accounts", Target: "/accounts"}}
	alert.ID = alertID(alert, "")
	return alert, true
}

// alertID is the stable id rule:subjectType:subjectId:window.
func alertID(alert Alert, window string) string {
	id := ""
	if alert.Subject.ID != nil {
		id = *alert.Subject.ID
	}
	return fmt.Sprintf("%s:%s:%s:%s", alert.Rule, alert.Subject.Type, id, window)
}

// collapseTargets are the filtered lists a summary alert opens.
var collapseTargets = map[AlertRule]string{
	AlertAccountReauth: "/accounts?status=reauth_required",
	AlertQuotaRunway:   "/groups",
	AlertKeyLimit:      "/keys?near_limit=1",
	AlertKeyErrorSpike: "/requests?status=error&timeframe=1h",
	AlertKeyExpiry:     "/keys?expiring_days=7",
}

// CollapseAlerts deduplicates per-group copies, replaces more than five
// subjects of one rule and severity with one summary and sorts the result.
func CollapseAlerts(raw []Alert) []Alert {
	seen := map[string]bool{}
	var unique []Alert
	for _, alert := range raw {
		if seen[alert.ID] {
			continue
		}
		seen[alert.ID] = true
		alert.Group, alert.Pool = nil, false
		unique = append(unique, alert)
	}
	type bucket struct {
		rule     AlertRule
		severity Severity
	}
	buckets := map[bucket][]Alert{}
	var order []bucket
	for _, alert := range unique {
		key := bucket{alert.Rule, alert.Severity}
		if _, ok := buckets[key]; !ok {
			order = append(order, key)
		}
		buckets[key] = append(buckets[key], alert)
	}
	result := []Alert{}
	for _, key := range order {
		group := buckets[key]
		target, collapsible := collapseTargets[key.rule]
		if len(group) <= AlertCollapseAbove || !collapsible {
			result = append(result, group...)
			continue
		}
		sortAlerts(group)
		top := group[0]
		summary := Alert{ID: fmt.Sprintf("%s:summary:%s", key.rule, key.severity), Rule: key.rule, Severity: key.severity,
			Subject: AlertSubject{Type: top.Subject.Type, Name: ""}, Value: top.Value, At: top.At, Count: len(group),
			Action: AlertAction{Kind: "open_list", Target: target}}
		result = append(result, summary)
	}
	sortAlerts(result)
	return result
}

// sortAlerts orders critical first, then by rule order, then by value: the
// earliest time first for runway and expiry, otherwise the highest value.
func sortAlerts(alerts []Alert) {
	sort.SliceStable(alerts, func(i, j int) bool {
		a, b := alerts[i], alerts[j]
		if severityRank(a.Severity) != severityRank(b.Severity) {
			return severityRank(a.Severity) < severityRank(b.Severity)
		}
		if alertRuleOrder[a.Rule] != alertRuleOrder[b.Rule] {
			return alertRuleOrder[a.Rule] < alertRuleOrder[b.Rule]
		}
		switch a.Rule {
		case AlertQuotaRunway, AlertKeyExpiry:
			if c := compareTimes(a.At, b.At); c != 0 {
				return c < 0
			}
			if c := compareFloats(a.Value, b.Value); c != 0 {
				return c < 0
			}
		default:
			if c := compareFloats(a.Value, b.Value); c != 0 {
				return c > 0
			}
		}
		if a.Subject.Name != b.Subject.Name {
			return a.Subject.Name < b.Subject.Name
		}
		return a.ID < b.ID
	})
}

// compareTimes orders nil last.
func compareTimes(a, b *time.Time) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return 1
	case b == nil:
		return -1
	}
	return a.Compare(*b)
}

// compareFloats compares known values; nil is lowest.
func compareFloats(a, b *float64) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return -1
	case b == nil:
		return 1
	case *a < *b:
		return -1
	case *a > *b:
		return 1
	}
	return 0
}

func percentPointer(rate *float64) *float64 {
	if rate == nil {
		return nil
	}
	return floatPointer(*rate * 100)
}

func stringPointer(value string) *string { return &value }
