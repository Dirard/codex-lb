package domain

// Retention defaults. Quota history is operational telemetry whose consumers
// read at most 30 days; request events are per-request financial evidence and
// stay until the operator configures request-log retention. Zero disables.
const (
	DefaultUsageHistoryRetentionDays = 45
	MinUsageHistoryRetentionDays     = 45
	MinRequestLogRetentionDays       = 30
	MaxRetentionDays                 = 3650
)

// EffectiveRequestLogRetentionDays returns the stored override; no override
// keeps request events.
func (s RuntimeSettings) EffectiveRequestLogRetentionDays() int {
	if s.RequestLogRetentionDays == nil {
		return 0
	}
	return *s.RequestLogRetentionDays
}

// EffectiveUsageHistoryRetentionDays returns the stored override or the
// 45-day default.
func (s RuntimeSettings) EffectiveUsageHistoryRetentionDays() int {
	if s.UsageHistoryRetentionDays == nil {
		return DefaultUsageHistoryRetentionDays
	}
	return *s.UsageHistoryRetentionDays
}
