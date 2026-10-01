package application

import (
	"slices"
	"time"

	"codex-lb/internal/domain"
)

func mergeAdditionalQuotas(accountID string, quotas []AdditionalQuota, observedAt time.Time) []domain.AccountAdditionalQuota {
	merged := make(map[string]domain.AccountAdditionalQuota)
	for _, quota := range quotas {
		key := domain.CanonicalAdditionalQuotaKey("", quota.LimitName, quota.MeteredFeature)
		if key == "" {
			continue
		}
		for _, entry := range []struct {
			window string
			usage  *UsageWindow
		}{{"primary", quota.Primary}, {"secondary", quota.Secondary}} {
			if entry.usage == nil || entry.usage.UsedPercent == nil {
				continue
			}
			candidate := domain.AccountAdditionalQuota{
				AccountID: accountID, QuotaKey: key, LimitName: quota.LimitName,
				MeteredFeature: quota.MeteredFeature, Window: entry.window,
				UsedPercent: clampPercent(*entry.usage.UsedPercent), ResetAt: entry.usage.ResetAt,
				WindowMinutes: entry.usage.WindowMinutes, ObservedAt: observedAt,
			}
			identity := key + "\x00" + entry.window
			previous, ok := merged[identity]
			if !ok || candidate.UsedPercent > previous.UsedPercent ||
				candidate.UsedPercent == previous.UsedPercent && (candidate.LimitName < previous.LimitName ||
					candidate.LimitName == previous.LimitName && candidate.MeteredFeature < previous.MeteredFeature) {
				merged[identity] = candidate
			}
		}
	}
	keys := make([]string, 0, len(merged))
	for key := range merged {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	result := make([]domain.AccountAdditionalQuota, 0, len(keys))
	for _, key := range keys {
		result = append(result, merged[key])
	}
	return result
}
