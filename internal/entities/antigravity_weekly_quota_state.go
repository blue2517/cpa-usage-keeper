package entities

import "time"

// AntigravityWeeklyQuotaConfidence marks how trustworthy a weekly quota estimate is.
type AntigravityWeeklyQuotaConfidence string

const (
	// AntigravityWeeklyQuotaConfidenceBootstrap means no weekly cap has been observed yet;
	// only the running accumulation is meaningful.
	AntigravityWeeklyQuotaConfidenceBootstrap AntigravityWeeklyQuotaConfidence = "bootstrap"
	// AntigravityWeeklyQuotaConfidenceLocked means a weekly cap was observed and the
	// estimated limit reflects the accumulated USD at the moment of exhaustion.
	AntigravityWeeklyQuotaConfidenceLocked AntigravityWeeklyQuotaConfidence = "locked"
)

// AntigravityWeeklyQuotaState tracks the heuristically estimated weekly quota for one
// (auth_index, pool) pair. Antigravity's weekly limit is a black box that can only be
// measured at the moment of exhaustion (a 429 QUOTA_EXHAUSTED). The accumulated USD spend
// since WindowStart is locked in as the estimated weekly limit when exhaustion is observed.
type AntigravityWeeklyQuotaState struct {
	ID        int64  `gorm:"primaryKey"`
	AuthIndex string `gorm:"not null;uniqueIndex:uniq_antigravity_weekly_quota_state_auth_pool,priority:1"`
	// Pool is "gemini" or "third_party"; the two pools have independent weekly caps.
	Pool string `gorm:"not null;uniqueIndex:uniq_antigravity_weekly_quota_state_auth_pool,priority:2"`
	// WindowStart is the start of the current weekly window the running spend accumulates from.
	WindowStart time.Time `gorm:"serializer:storageTime;not null"`
	// EstimatedLimitUSD is the locked weekly limit estimate; nil until a cap is observed.
	EstimatedLimitUSD *float64
	// LastExhaustedAt is the most recent weekly-cap exhaustion timestamp.
	LastExhaustedAt *time.Time `gorm:"serializer:storageTime"`
	// LastResetAt is the projected weekly window reset time (when the cap should clear).
	LastResetAt *time.Time `gorm:"serializer:storageTime"`
	// Confidence is "bootstrap" or "locked".
	Confidence string    `gorm:"not null;default:'bootstrap'"`
	CreatedAt  time.Time `gorm:"serializer:storageTime;not null"`
	UpdatedAt  time.Time `gorm:"serializer:storageTime;not null"`
}
