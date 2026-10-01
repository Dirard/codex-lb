package domain

import "time"

type WeeklyCreditPaceStatus string
type WeeklyCreditRunwayStatus string
type WeeklyCreditConfidence string

const (
	WeeklyCreditPaceAhead   WeeklyCreditPaceStatus = "ahead"
	WeeklyCreditPaceOnTrack WeeklyCreditPaceStatus = "on_track"
	WeeklyCreditPaceDanger  WeeklyCreditPaceStatus = "danger"

	WeeklyCreditRunwaySafe    WeeklyCreditRunwayStatus = "safe"
	WeeklyCreditRunwayTight   WeeklyCreditRunwayStatus = "tight"
	WeeklyCreditRunwayRunsDry WeeklyCreditRunwayStatus = "runs_dry"

	WeeklyCreditConfidenceHigh   WeeklyCreditConfidence = "high"
	WeeklyCreditConfidenceMedium WeeklyCreditConfidence = "medium"
	WeeklyCreditConfidenceLow    WeeklyCreditConfidence = "low"
)

type WeeklyCreditResetEvent struct {
	At              time.Time `json:"at"`
	CreditsReturned float64   `json:"creditsReturned"`
}

type WeeklyCreditAPIKeyAttribution struct {
	APIKeyID       *string `json:"apiKeyId"`
	Name           string  `json:"name"`
	Requests       int64   `json:"requests"`
	BillableTokens int64   `json:"billableTokens"`
	CachedTokens   int64   `json:"cachedTokens"`
	DominantModel  string  `json:"dominantModel"`
}

type WeeklyCreditPace struct {
	TotalFullCredits                    float64                         `json:"totalFullCredits"`
	TotalActualRemainingCredits         float64                         `json:"totalActualRemainingCredits"`
	TotalExpectedRemainingCredits       float64                         `json:"totalExpectedRemainingCredits"`
	ActualUsedPercent                   float64                         `json:"actualUsedPercent"`
	ScheduledUsedPercent                float64                         `json:"scheduledUsedPercent"`
	DeltaPercent                        float64                         `json:"deltaPercent"`
	ScheduleGapCredits                  float64                         `json:"scheduleGapCredits"`
	SmoothedDeltaPercent                float64                         `json:"smoothedDeltaPercent"`
	SmoothedScheduleGapCredits          float64                         `json:"smoothedScheduleGapCredits"`
	PaceGapSmoothingMinutes             int                             `json:"paceGapSmoothingMinutes"`
	OverPlanCredits                     float64                         `json:"overPlanCredits"`
	ProjectedShortfallCredits           float64                         `json:"projectedShortfallCredits"`
	PauseForBreakEvenHours              *float64                        `json:"pauseForBreakEvenHours"`
	PaceMultiplier                      *float64                        `json:"paceMultiplier"`
	ThrottleToPercent                   *float64                        `json:"throttleToPercent"`
	ReduceByPercent                     *float64                        `json:"reduceByPercent"`
	ProAccountEquivalentToCoverOverPlan *float64                        `json:"proAccountEquivalentToCoverOverPlan"`
	ProAccountsToCoverOverPlan          *int                            `json:"proAccountsToCoverOverPlan"`
	ProjectedDepletionHours             *float64                        `json:"projectedDepletionHours"`
	ProjectedMinimumRemainingCredits    *float64                        `json:"projectedMinimumRemainingCredits"`
	ForecastBurnRateCreditsPerHour      *float64                        `json:"forecastBurnRateCreditsPerHour"`
	ScheduledBurnRateCreditsPerHour     float64                         `json:"scheduledBurnRateCreditsPerHour"`
	HeadroomPercent                     float64                         `json:"headroomPercent"`
	HeadroomCredits                     float64                         `json:"headroomCredits"`
	BurnRateRecentCreditsPerHour        *float64                        `json:"burnRateRecentCreditsPerHour"`
	DepletionETAHours                   *float64                        `json:"depletionEtaHours"`
	NextReliefInHours                   float64                         `json:"nextReliefInHours"`
	NextReliefCredits                   float64                         `json:"nextReliefCredits"`
	ResetEvents                         []WeeklyCreditResetEvent        `json:"resetEvents"`
	RunwayStatus                        WeeklyCreditRunwayStatus        `json:"runwayStatus"`
	SaturatedAccountCount               int                             `json:"saturatedAccountCount"`
	TopAPIKeys                          []WeeklyCreditAPIKeyAttribution `json:"topApiKeys"`
	AddProAccounts                      *int                            `json:"addProAccounts"`
	Status                              WeeklyCreditPaceStatus          `json:"status"`
	AccountCount                        int                             `json:"accountCount"`
	StaleAccountCount                   int                             `json:"staleAccountCount"`
	InactiveAccountCount                int                             `json:"inactiveAccountCount"`
	Confidence                          WeeklyCreditConfidence          `json:"confidence"`
}
