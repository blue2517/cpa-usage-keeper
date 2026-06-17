package quota

import (
	"context"
	"time"

	"cpa-usage-keeper/internal/repository"
	"cpa-usage-keeper/internal/timeutil"

	"github.com/sirupsen/logrus"
)

const quotaWindowWeeklySeconds int64 = 7 * 24 * 60 * 60

// appendAntigravityWeeklyRows reads the persisted weekly quota state for an auth
// and appends one weekly-estimate row per pool that has data.
func (s *Service) appendAntigravityWeeklyRows(ctx context.Context, authIndex string, rows []QuotaRow) []QuotaRow {
	if s.db == nil {
		return rows
	}
	states, err := repository.ListAntigravityWeeklyQuotaStatesByAuthIndex(ctx, s.db, authIndex)
	if err != nil {
		logrus.WithError(err).WithField("auth_index", authIndex).Warn("failed to load antigravity weekly quota states")
		return rows
	}
	if len(states) == 0 {
		return rows
	}

	now := timeutil.NormalizeStorageTime(time.Now())
	for _, state := range states {
		pool := AntigravityPool(state.Pool)
		windowStart := timeutil.NormalizeStorageTime(state.WindowStart)
		if windowStart.IsZero() {
			windowStart = now.Add(-antigravityWeeklyWindowDefault)
		}

		geminiPool := pool == AntigravityPoolGemini
		stats, err := repository.SumUsageWindowStatsByAuthIndexAndPoolGemini(ctx, s.db, authIndex, windowStart, &now, geminiPool)
		if err != nil {
			logrus.WithError(err).WithFields(logrus.Fields{
				"auth_index": authIndex,
				"pool":       string(pool),
			}).Warn("failed to compute antigravity weekly pool cost")
			continue
		}

		row := QuotaRow{
			Key:               "weekly." + string(pool),
			Label:             antigravityPoolLabel(pool) + " Weekly",
			Scope:             "window",
			Metric:            string(pool) + "_weekly",
			Window:            &QuotaWindow{Seconds: intPtr(quotaWindowWeeklySeconds)},
			WindowUsageCost:   floatPtr(stats.Cost),
			WindowUsageTokens: intPtr(stats.Tokens),
		}

		if state.EstimatedLimitUSD != nil && *state.EstimatedLimitUSD > 0 {
			row.Limit = state.EstimatedLimitUSD
			usedPercent := stats.Cost / *state.EstimatedLimitUSD * 100
			if usedPercent > 100 {
				usedPercent = 100
			}
			row.UsedPercent = floatPtr(usedPercent)
		}

		if state.LastResetAt != nil {
			resetAt := timeutil.NormalizeStorageTime(*state.LastResetAt)
			if !resetAt.IsZero() {
				row.ResetAt = resetAt.UTC().Format(time.RFC3339)
			}
		}

		rows = append(rows, row)
	}
	return rows
}
