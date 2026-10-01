package domain

import "time"

type DashboardTimeframe struct {
	Key           string `json:"key"`
	WindowMinutes int    `json:"windowMinutes"`
	BucketSeconds int    `json:"bucketSeconds"`
	BucketCount   int    `json:"bucketCount"`
}

type TrendPoint struct {
	T time.Time `json:"t"`
	V float64   `json:"v"`
}

type DashboardTrends struct {
	Requests      []TrendPoint `json:"requests"`
	Tokens        []TrendPoint `json:"tokens"`
	Cost          []TrendPoint `json:"cost"`
	ErrorRate     []TrendPoint `json:"errorRate"`
	Conversations []TrendPoint `json:"conversations"`
}

type DashboardMetrics struct {
	Requests             *float64 `json:"requests"`
	Tokens               *float64 `json:"tokens"`
	CachedInputTokens    *float64 `json:"cachedInputTokens"`
	ErrorRate            *float64 `json:"errorRate"`
	ErrorCount           *float64 `json:"errorCount"`
	CancelledCount       *int64   `json:"cancelledCount"`
	TopError             *string  `json:"topError"`
	Conversations        *int64   `json:"conversations"`
	ConversationRequests int64    `json:"conversationRequests"`
}

type DashboardComparison struct {
	CanCompare bool `json:"canCompare"`
	Previous   struct {
		Requests int64   `json:"requests"`
		Tokens   int64   `json:"tokens"`
		CostUSD  float64 `json:"costUsd"`
	} `json:"previous"`
}

type DashboardUsageSummaryWindow struct {
	RemainingPercent float64    `json:"remainingPercent"`
	CapacityCredits  float64    `json:"capacityCredits"`
	RemainingCredits float64    `json:"remainingCredits"`
	ResetAt          *time.Time `json:"resetAt"`
	WindowMinutes    *int       `json:"windowMinutes"`
}

type DashboardUsageAccount struct {
	AccountID           string   `json:"accountId"`
	RemainingPercentAvg *float64 `json:"remainingPercentAvg"`
	CapacityCredits     float64  `json:"capacityCredits"`
	RemainingCredits    float64  `json:"remainingCredits"`
}

type DashboardUsageWindow struct {
	WindowKey     string                  `json:"windowKey"`
	WindowMinutes *int                    `json:"windowMinutes"`
	Accounts      []DashboardUsageAccount `json:"accounts"`
}

type DashboardAccount struct {
	Account
	Usage struct {
		PrimaryRemainingPercent   *float64 `json:"primaryRemainingPercent"`
		SecondaryRemainingPercent *float64 `json:"secondaryRemainingPercent"`
		MonthlyRemainingPercent   *float64 `json:"monthlyRemainingPercent"`
	} `json:"usage"`
	ResetAtPrimary            *time.Time `json:"resetAtPrimary"`
	ResetAtSecondary          *time.Time `json:"resetAtSecondary"`
	ResetAtMonthly            *time.Time `json:"resetAtMonthly"`
	WindowMinutesPrimary      *int       `json:"windowMinutesPrimary"`
	WindowMinutesSecondary    *int       `json:"windowMinutesSecondary"`
	WindowMinutesMonthly      *int       `json:"windowMinutesMonthly"`
	CapacityCreditsPrimary    *float64   `json:"capacityCreditsPrimary"`
	CapacityCreditsSecondary  *float64   `json:"capacityCreditsSecondary"`
	CapacityCreditsMonthly    *float64   `json:"capacityCreditsMonthly"`
	RemainingCreditsPrimary   *float64   `json:"remainingCreditsPrimary"`
	RemainingCreditsSecondary *float64   `json:"remainingCreditsSecondary"`
	RemainingCreditsMonthly   *float64   `json:"remainingCreditsMonthly"`
	CreditsHas                *bool      `json:"creditsHas"`
	CreditsUnlimited          *bool      `json:"creditsUnlimited"`
	CreditsBalance            *float64   `json:"creditsBalance"`
	RequestUsage              struct {
		RequestCount      int64   `json:"requestCount"`
		TotalTokens       int64   `json:"totalTokens"`
		CachedInputTokens int64   `json:"cachedInputTokens"`
		TotalCostUSD      float64 `json:"totalCostUsd"`
	} `json:"requestUsage"`
}

type DashboardAdditionalQuota struct {
	QuotaKey       *string `json:"quotaKey"`
	LimitName      string  `json:"limitName"`
	MeteredFeature string  `json:"meteredFeature"`
}

type DashboardOverview struct {
	LastSyncAt *time.Time         `json:"lastSyncAt"`
	Timeframe  DashboardTimeframe `json:"timeframe"`
	Accounts   []DashboardAccount `json:"accounts"`
	Summary    struct {
		PrimaryWindow   DashboardUsageSummaryWindow  `json:"primaryWindow"`
		SecondaryWindow *DashboardUsageSummaryWindow `json:"secondaryWindow"`
		Cost            struct {
			Currency string  `json:"currency"`
			TotalUSD float64 `json:"totalUsd"`
		} `json:"cost"`
		Metrics    *DashboardMetrics   `json:"metrics"`
		Comparison DashboardComparison `json:"comparison"`
	} `json:"summary"`
	Windows struct {
		Primary   DashboardUsageWindow  `json:"primary"`
		Secondary *DashboardUsageWindow `json:"secondary"`
	} `json:"windows"`
	Trends           DashboardTrends            `json:"trends"`
	AdditionalQuotas []DashboardAdditionalQuota `json:"additionalQuotas"`
	WeeklyCreditPace *WeeklyCreditPace          `json:"weeklyCreditPace"`
}

type DashboardDepletion struct {
	Risk                   float64    `json:"risk"`
	RiskLevel              string     `json:"riskLevel"`
	BurnRate               float64    `json:"burnRate"`
	SafeUsagePercent       float64    `json:"safeUsagePercent"`
	ProjectedExhaustionAt  *time.Time `json:"projectedExhaustionAt"`
	SecondsUntilExhaustion *float64   `json:"secondsUntilExhaustion"`
}

type DashboardProjections struct {
	DepletionPrimary   *DashboardDepletion `json:"depletionPrimary"`
	DepletionSecondary *DashboardDepletion `json:"depletionSecondary"`
	WeeklyCreditPace   *WeeklyCreditPace   `json:"weeklyCreditPace"`
}

type DashboardTrafficBucket struct {
	At                                                                time.Time
	Requests, Input, Output, Cached, Errors, Cancelled, Conversations int64
	CostUSD                                                           float64
}

type DashboardTraffic struct {
	Current, Previous                                                           UsageTotals
	CurrentCostUSD, PreviousCostUSD                                             float64
	CurrentErrors, CurrentCancelled, CurrentConversations, ConversationRequests int64
	TopError                                                                    *string
	CanCompare                                                                  bool
	Buckets                                                                     []DashboardTrafficBucket
}
