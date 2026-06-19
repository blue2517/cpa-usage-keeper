package quota

import (
	"context"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository"
	"cpa-usage-keeper/internal/timeutil"

	"github.com/sirupsen/logrus"
)

const quotaWindowWeeklySeconds int64 = 7 * 24 * 60 * 60

// appendAntigravityWeeklyRows appends one weekly row per Antigravity pool. A pool with a
// persisted (locked) cap renders an estimate-vs-cap bar; pools without a cap yet render a
// bootstrap row showing the running last-7d spend (no percent) so the weekly usage is visible
// before the first 429. The display window is rolled forward by whole weeks so a window whose
// projected reset has elapsed restarts from zero instead of accumulating across resets.
func (s *Service) appendAntigravityWeeklyRows(ctx context.Context, authIndex string, rows []QuotaRow) []QuotaRow {
	if s.db == nil {
		return rows
	}
	states, err := repository.ListAntigravityWeeklyQuotaStatesByAuthIndex(ctx, s.db, authIndex)
	if err != nil {
		logrus.WithError(err).WithField("auth_index", authIndex).Warn("failed to load antigravity weekly quota states")
		return rows
	}
	statesByPool := make(map[AntigravityPool]entities.AntigravityWeeklyQuotaState, len(states))
	for _, state := range states {
		statesByPool[AntigravityPool(state.Pool)] = state
	}

	now := timeutil.NormalizeStorageTime(time.Now())
	for _, pool := range []AntigravityPool{AntigravityPoolGemini, AntigravityPoolThirdParty} {
		state, locked := statesByPool[pool]

		var windowStart, resetAt time.Time
		if locked {
			windowStart, resetAt = currentAntigravityWeeklyWindow(&state, now)
		} else {
			// No cap observed yet: show the trailing 7d spend as a bootstrap signal.
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

		// Bootstrap rows are only worth showing once the pool has any spend; an idle pool with
		// no cap and no usage adds noise.
		if !locked && stats.Tokens == 0 && stats.Cost == 0 {
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

		if locked && state.EstimatedLimitUSD != nil && *state.EstimatedLimitUSD > 0 {
			row.Limit = state.EstimatedLimitUSD
			usedPercent := stats.Cost / *state.EstimatedLimitUSD * 100
			if usedPercent > 100 {
				usedPercent = 100
			}
			row.UsedPercent = floatPtr(usedPercent)
		}

		if !resetAt.IsZero() {
			row.ResetAt = resetAt.UTC().Format(time.RFC3339)
		}

		rows = append(rows, row)
	}
	return rows
}
