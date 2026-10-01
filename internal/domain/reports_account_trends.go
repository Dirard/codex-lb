package domain

import "time"

type AccountQuotaTrendBucket struct {
	Window        string
	At            time.Time
	ObservedAt    time.Time
	UsedPercent   float64
	ResetAt       *time.Time
	WindowMinutes *int
}

type AccountTrendsResponse struct {
	AccountID          string       `json:"accountId"`
	Primary            []TrendPoint `json:"primary"`
	Secondary          []TrendPoint `json:"secondary"`
	SecondaryScheduled []TrendPoint `json:"secondaryScheduled"`
}
