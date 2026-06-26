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

// appendAntigravityWeeklyRows appends one weekly row per Antigravity pool.
//
// Source priority:
//  1. Live weekly buckets from retrieveUserQuotaSummary (liveBuckets) — the authoritative
//     remaining fraction and absolute reset time reported by upstream. When present, used%
//     and reset time come straight from upstream, the window is [reset-7d, reset], and the
//     weekly USD cap is back-solved from the recorded spend over that window
//     (cap = spend / usedFraction). The measured cap is persisted so the fallback path below
//     stays warm when a later summary call fails.
//  2. A persisted (locked) cap from the 429 QUOTA_EXHAUSTED detector — used only when no live
//     bucket is available.
//  3. A bootstrap row showing the trailing 7d spend (no percent) so weekly usage is visible
//     before either signal exists.
//
// The display window is rolled forward by whole weeks so a window whose projected reset has
// elapsed restarts from zero instead of accumulating across resets.
func (s *Service) appendAntigravityWeeklyRows(ctx context.Context, authIndex string, rows []QuotaRow, liveBuckets map[AntigravityPool]AntigravityWeeklyBucket) []QuotaRow {
	if s.db == nil {
		return rows
	}
	states, err := repository.ListAntigravityWeeklyQuotaStatesByAuthIndex(ctx, s.db, authIndex)
	if err != nil {
		// 读不到历史状态时仍可用实时桶渲染，因此只告警不中断。
		logrus.WithError(err).WithField("auth_index", authIndex).Warn("failed to load antigravity weekly quota states")
	}
	statesByPool := make(map[AntigravityPool]entities.AntigravityWeeklyQuotaState, len(states))
	for _, state := range states {
		statesByPool[AntigravityPool(state.Pool)] = state
	}

	now := timeutil.NormalizeStorageTime(time.Now())
	for _, pool := range []AntigravityPool{AntigravityPoolGemini, AntigravityPoolThirdParty} {
		state, locked := statesByPool[pool]
		live, hasLive := liveBuckets[pool]

		var windowStart, resetAt time.Time
		switch {
		case hasLive:
			windowStart, resetAt = antigravityLiveWeeklyWindow(live, now)
		case locked:
			windowStart, resetAt = currentAntigravityWeeklyWindow(&state, now)
		default:
			// 无 cap 也无实时数据：展示最近 7 天用量作为引导信号。
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
		// no live data and no cap and no usage adds noise.
		if !hasLive && !locked && stats.Tokens == 0 && stats.Cost == 0 {
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

		if hasLive {
			s.applyLiveWeeklyQuota(ctx, authIndex, pool, &row, live, stats, windowStart, resetAt, &state, locked)
		} else if locked && state.EstimatedLimitUSD != nil && *state.EstimatedLimitUSD > 0 {
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

// applyLiveWeeklyQuota fills a weekly row from the upstream-reported remaining fraction: used%
// is authoritative, and the weekly USD cap is back-solved from the recorded spend over the
// current window. When the window is partially consumed (0 < used% < 100) the measured cap is
// persisted so the 429/exhausted fallback can reuse it; once exhausted (used% ≥ 100) the cap
// can't be measured from the ratio, so the last persisted cap is reused if available.
func (s *Service) applyLiveWeeklyQuota(
	ctx context.Context,
	authIndex string,
	pool AntigravityPool,
	row *QuotaRow,
	live AntigravityWeeklyBucket,
	stats repository.UsageWindowStats,
	windowStart time.Time,
	resetAt time.Time,
	state *entities.AntigravityWeeklyQuotaState,
	locked bool,
) {
	remainingFraction := clampFraction(live.RemainingFraction)
	usedFraction := 1 - remainingFraction
	row.UsedPercent = floatPtr(usedFraction * 100)
	row.RemainingFraction = floatPtr(remainingFraction)

	// 周期已被消耗一部分（非满额、非耗尽）时才能用 已用%反推整周 USD 上限。
	if usedFraction > 0 && usedFraction < 1 && stats.Cost > 0 {
		weeklyCap := stats.Cost / usedFraction
		if weeklyCap > 0 {
			row.Limit = floatPtr(weeklyCap)
			s.persistAntigravityWeeklyCap(ctx, authIndex, pool, weeklyCap, windowStart, resetAt, state, locked)
		}
		return
	}

	// 已耗尽或无法反推时，复用最近一次持久化的上限（来自实时反推或 429 测量）。
	if locked && state != nil && state.EstimatedLimitUSD != nil && *state.EstimatedLimitUSD > 0 {
		row.Limit = state.EstimatedLimitUSD
	}
}

// persistAntigravityWeeklyCap stores a live-measured weekly cap as the locked estimate so the
// fallback path keeps a real number when a later summary call fails. WindowStart/LastResetAt
// track the live window so the 429 detector's locked-window guard sees the real reset time.
func (s *Service) persistAntigravityWeeklyCap(
	ctx context.Context,
	authIndex string,
	pool AntigravityPool,
	weeklyCap float64,
	windowStart time.Time,
	resetAt time.Time,
	state *entities.AntigravityWeeklyQuotaState,
	locked bool,
) {
	next := entities.AntigravityWeeklyQuotaState{
		AuthIndex:         authIndex,
		Pool:              string(pool),
		WindowStart:       windowStart,
		EstimatedLimitUSD: &weeklyCap,
		Confidence:        string(entities.AntigravityWeeklyQuotaConfidenceLocked),
	}
	if !resetAt.IsZero() {
		reset := resetAt
		next.LastResetAt = &reset
	}
	// 保留历史里已有的最近耗尽时间，避免实时刷新把 429 记录抹掉。
	if locked && state != nil {
		next.LastExhaustedAt = state.LastExhaustedAt
	}
	if err := repository.UpsertAntigravityWeeklyQuotaState(ctx, s.db, next); err != nil {
		logrus.WithError(err).WithFields(logrus.Fields{
			"auth_index": authIndex,
			"pool":       string(pool),
		}).Warn("failed to persist antigravity weekly cap from live quota")
	}
}

// antigravityLiveWeeklyWindow derives the [start, reset) weekly window from a live bucket's
// absolute reset time. When the reset time is missing or unparseable it falls back to a
// trailing 7d window ending at now with an unknown (zero) reset.
func antigravityLiveWeeklyWindow(bucket AntigravityWeeklyBucket, now time.Time) (start time.Time, reset time.Time) {
	if resetAt, err := timeutil.ParseStorageTime(bucket.ResetTime); err == nil {
		reset = timeutil.NormalizeStorageTime(resetAt)
		start = reset.Add(-antigravityWeeklyWindowDefault)
		return start, reset
	}
	return now.Add(-antigravityWeeklyWindowDefault), time.Time{}
}

// clampFraction constrains a remaining fraction to the [0, 1] interval.
func clampFraction(fraction float64) float64 {
	if fraction < 0 {
		return 0
	}
	if fraction > 1 {
		return 1
	}
	return fraction
}
