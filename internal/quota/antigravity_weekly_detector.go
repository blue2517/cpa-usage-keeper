package quota

import (
	"context"
	"fmt"
	"strings"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository"
	"cpa-usage-keeper/internal/timeutil"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

const (
	antigravityExecutorType          = "AntigravityExecutor"
	antigravityWeeklyWindowDefault   = 7 * 24 * time.Hour
	antigravityQuotaExhaustedKeyword = "QUOTA_EXHAUSTED"
)

// AntigravityWeeklyExhaustionEvent carries the information needed for weekly-cap detection.
type AntigravityWeeklyExhaustionEvent struct {
	AuthIndex      string
	Model          string
	Timestamp      time.Time
	FailStatusCode int
	FailBody       string
	ExecutorType   string
}

// IsAntigravityWeeklyExhaustion returns true if the event represents an Antigravity
// weekly quota exhaustion (429 with QUOTA_EXHAUSTED in the body).
func IsAntigravityWeeklyExhaustion(e AntigravityWeeklyExhaustionEvent) bool {
	if !strings.EqualFold(strings.TrimSpace(e.ExecutorType), antigravityExecutorType) {
		return false
	}
	if e.FailStatusCode != 429 {
		return false
	}
	return strings.Contains(strings.ToUpper(e.FailBody), antigravityQuotaExhaustedKeyword)
}

// HandleAntigravityWeeklyExhaustion detects weekly quota exhaustion from an ingestion event
// and locks the accumulated USD as the estimated weekly limit. It is safe to call for every
// antigravity event — non-exhaustion events are ignored.
func HandleAntigravityWeeklyExhaustion(ctx context.Context, db *gorm.DB, e AntigravityWeeklyExhaustionEvent) error {
	if !IsAntigravityWeeklyExhaustion(e) {
		return nil
	}
	pool := ClassifyAntigravityPool(e.Model)
	now := timeutil.NormalizeStorageTime(e.Timestamp)
	if now.IsZero() {
		now = timeutil.NormalizeStorageTime(time.Now())
	}

	state, err := repository.GetAntigravityWeeklyQuotaState(ctx, db, e.AuthIndex, string(pool))
	if err != nil {
		return fmt.Errorf("load antigravity weekly quota state: %w", err)
	}

	windowStart := resolveWeeklyWindowStart(state, now)
	accumulatedUSD, err := accumulatePoolUSD(ctx, db, e.AuthIndex, pool, windowStart, &now)
	if err != nil {
		return fmt.Errorf("accumulate antigravity pool USD: %w", err)
	}

	nextReset := now.Add(antigravityWeeklyWindowDefault)

	logrus.WithFields(logrus.Fields{
		"auth_index":      e.AuthIndex,
		"pool":            string(pool),
		"accumulated_usd": accumulatedUSD,
		"window_start":    windowStart,
		"next_reset":      nextReset,
	}).Info("antigravity weekly quota exhaustion detected, locking estimate")

	return repository.UpsertAntigravityWeeklyQuotaState(ctx, db, entities.AntigravityWeeklyQuotaState{
		AuthIndex:         e.AuthIndex,
		Pool:              string(pool),
		WindowStart:       nextReset,
		EstimatedLimitUSD: &accumulatedUSD,
		LastExhaustedAt:   &now,
		LastResetAt:       &nextReset,
		Confidence:        string(entities.AntigravityWeeklyQuotaConfidenceLocked),
	})
}

func resolveWeeklyWindowStart(state *entities.AntigravityWeeklyQuotaState, now time.Time) time.Time {
	if state != nil && !state.WindowStart.IsZero() {
		ws := timeutil.NormalizeStorageTime(state.WindowStart)
		if ws.Before(now) {
			return ws
		}
	}
	return now.Add(-antigravityWeeklyWindowDefault)
}

func accumulatePoolUSD(ctx context.Context, db *gorm.DB, authIndex string, pool AntigravityPool, start time.Time, end *time.Time) (float64, error) {
	geminiPool := pool == AntigravityPoolGemini
	stats, err := repository.SumUsageWindowStatsByAuthIndexAndPoolGemini(ctx, db, authIndex, start, end, geminiPool)
	if err != nil {
		return 0, err
	}
	return stats.Cost, nil
}
