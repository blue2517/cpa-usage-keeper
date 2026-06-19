package entities

import "time"

// AntigravityFiveHourEstimateState caches the most recent projected full-cycle usage
// for one (auth_index, pool) Antigravity 5h pool. A 5h cycle can only be extrapolated
// once some of it has been consumed (0% < used% < 100%); right after a reset the live
// estimate is undefined. This row preserves the previous cycle's projection so the UI
// can keep showing an estimate while the new cycle is still at 100% remaining.
type AntigravityFiveHourEstimateState struct {
	ID        int64  `gorm:"primaryKey"`
	AuthIndex string `gorm:"not null;uniqueIndex:uniq_antigravity_five_hour_estimate_auth_pool,priority:1"`
	// Pool is "gemini" or "third_party"; the two pools meter independently.
	Pool string `gorm:"not null;uniqueIndex:uniq_antigravity_five_hour_estimate_auth_pool,priority:2"`
	// EstimatedTokens is the projected total tokens for a full 5h cycle.
	EstimatedTokens int64
	// EstimatedCostUSD is the projected total cost for a full 5h cycle.
	EstimatedCostUSD float64
	// CycleResetAt is the reset time of the cycle the estimate was computed from.
	CycleResetAt *time.Time `gorm:"serializer:storageTime"`
	CreatedAt    time.Time  `gorm:"serializer:storageTime;not null"`
	UpdatedAt    time.Time  `gorm:"serializer:storageTime;not null"`
}
