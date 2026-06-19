package quota

import (
	"context"
	"strings"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository"
	"cpa-usage-keeper/internal/timeutil"

	"github.com/sirupsen/logrus"
)

// attachAntigravityFiveHourEstimates persists the projected full-cycle usage for each
// Antigravity 5h pool row whose current cycle can be extrapolated, and attaches the most
// recent persisted projection to every pool row as a fallback estimate. This lets the UI keep
// showing an estimate during a fresh cycle (≈100% remaining) where the live spend cannot be
// extrapolated. Must run after attachWindowUsageStats so window token/cost are populated.
func (s *Service) attachAntigravityFiveHourEstimates(ctx context.Context, authIndex string, response CheckResponse, now time.Time) CheckResponse {
	if s.db == nil || len(response.Quota) == 0 {
		return response
	}
	if !hasAntigravityPoolRow(response.Quota) {
		return response
	}

	states, err := repository.ListAntigravityFiveHourEstimateStatesByAuthIndex(ctx, s.db, authIndex)
	if err != nil {
		logrus.WithError(err).WithField("auth_index", authIndex).Warn("failed to load antigravity 5h estimate states")
		return response
	}
	stored := make(map[AntigravityPool]entities.AntigravityFiveHourEstimateState, len(states))
	for _, state := range states {
		stored[AntigravityPool(state.Pool)] = state
	}

	now = timeutil.NormalizeStorageTime(now)
	for index := range response.Quota {
		row := response.Quota[index]
		if row.AntigravityGeminiPool == nil || !strings.HasPrefix(row.Key, "pool.") {
			continue
		}
		pool := AntigravityPoolThirdParty
		if *row.AntigravityGeminiPool {
			pool = AntigravityPoolGemini
		}

		// Expose the previously persisted projection as the fallback estimate before
		// (potentially) overwriting it with this cycle's projection below.
		if prev, ok := stored[pool]; ok {
			tokens := prev.EstimatedTokens
			cost := prev.EstimatedCostUSD
			response.Quota[index].PrevCycleUsageTokens = &tokens
			response.Quota[index].PrevCycleUsageCost = &cost
		}

		estTokens, estCost, ok := projectAntigravityCycleUsage(row)
		if !ok {
			continue
		}
		var cycleReset *time.Time
		if resetAt, err := timeutil.ParseStorageTime(row.ResetAt); err == nil {
			normalized := timeutil.NormalizeStorageTime(resetAt)
			cycleReset = &normalized
		}
		if err := repository.UpsertAntigravityFiveHourEstimateState(ctx, s.db, entities.AntigravityFiveHourEstimateState{
			AuthIndex:        authIndex,
			Pool:             string(pool),
			EstimatedTokens:  estTokens,
			EstimatedCostUSD: estCost,
			CycleResetAt:     cycleReset,
		}); err != nil {
			logrus.WithError(err).WithFields(logrus.Fields{
				"auth_index": authIndex,
				"pool":       string(pool),
			}).Warn("failed to persist antigravity 5h estimate")
		}
	}
	return response
}

func hasAntigravityPoolRow(rows []QuotaRow) bool {
	for _, row := range rows {
		if row.AntigravityGeminiPool != nil && strings.HasPrefix(row.Key, "pool.") {
			return true
		}
	}
	return false
}

// projectAntigravityCycleUsage extrapolates a pool row's partial-cycle spend to a full cycle
// using the displayed used%. It returns ok=false when the cycle is too fresh or fully consumed
// to extrapolate (used% outside the open (0,100) interval), or when no spend is recorded yet.
func projectAntigravityCycleUsage(row QuotaRow) (int64, float64, bool) {
	if row.UsedPercent == nil || row.WindowUsageTokens == nil || row.WindowUsageCost == nil {
		return 0, 0, false
	}
	usedPercent := *row.UsedPercent
	if usedPercent <= 0 || usedPercent >= 100 {
		return 0, 0, false
	}
	tokens := *row.WindowUsageTokens
	cost := *row.WindowUsageCost
	if tokens <= 0 || cost <= 0 {
		return 0, 0, false
	}
	ratio := usedPercent / 100
	return int64(float64(tokens) / ratio), cost / ratio, true
}
