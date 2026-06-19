package quota

import (
	"context"
	"strings"
	"time"

	"cpa-usage-keeper/internal/repository"
	"cpa-usage-keeper/internal/timeutil"
)

type quotaUsageWindowKey struct {
	start time.Time
	end   time.Time
	// pool 区分全模型统计("")与 Antigravity Gemini/第三方池谓词("gemini"/"third_party")。
	pool string
}

type usageWindowStatsProvider interface {
	SumByAuthIndex(context.Context, string, time.Time, *time.Time) (repository.UsageWindowStats, error)
	SumByAuthIndexAndPoolGemini(context.Context, string, time.Time, *time.Time, bool) (repository.UsageWindowStats, error)
}

func (s *Service) attachWindowUsageStats(ctx context.Context, authIndex string, response CheckResponse, now time.Time) CheckResponse {
	if s == nil {
		return response
	}
	calculator, err := repository.NewUsageWindowStatsCalculator(ctx, s.db)
	if err != nil {
		return response
	}
	return s.attachWindowUsageStatsWithProvider(ctx, authIndex, response, now, calculator)
}

func (s *Service) attachWindowUsageStatsWithProvider(ctx context.Context, authIndex string, response CheckResponse, now time.Time, statsProvider usageWindowStatsProvider) CheckResponse {
	// quota 为空时没有可补充的窗口用量，直接返回原响应。
	if len(response.Quota) == 0 || statsProvider == nil {
		// 返回原响应，避免后续 map 和数据库查询开销。
		return response
	}
	// 同一个 quota response 可能包含多个相同窗口，按 start/end 去重后再查询数据库。
	statsByWindow := make(map[quotaUsageWindowKey]repository.UsageWindowStats)
	// 遍历每一条 quota row，只对没有完整 provider 用量的普通窗口补 token/cost。
	for index := range response.Quota {
		// Pro 等上游可能已经返回窗口 token/cost；非 window scope 不用本地 auth 级统计兜底。
		if !shouldBackfillWindowUsageStats(response.Quota[index]) {
			// 保留 provider 原始字段，避免覆盖 additional/code_review 等专项限额。
			continue
		}
		// provider pair 不完整时先丢弃单边字段，避免 fallback 失败后输出不同口径的半套数据。
		response.Quota[index].WindowUsageTokens = nil
		response.Quota[index].WindowUsageCost = nil
		var windowStart, windowEnd time.Time
		var ok bool
		if response.Quota[index].AntigravityGeminiPool != nil {
			// Antigravity pool rows use a fixed lookback instead of the bottleneck model's
			// resetAt, which may belong to a different cycle and produce an inverted or
			// near-empty window (e.g. a model that just reset gives a 3-minute window, or
			// a weekly-reset model produces start > end).
			windowStart, windowEnd, ok = antigravityPoolUsageWindow(response.Quota[index], now)
		} else {
			windowStart, windowEnd, ok = quotaRowUsageWindow(response.Quota[index], now)
		}
		if !ok {
			continue
		}
		// Antigravity 池谓词参与缓存 key，避免不同池（如 Gemini / Claude·GPT）窗口统计互相串用。
		geminiPool := response.Quota[index].AntigravityGeminiPool
		// start/end + 池标识组成窗口缓存 key，避免同一响应内重复查同一窗口。
		key := quotaUsageWindowKey{start: windowStart, end: windowEnd, pool: antigravityPoolCacheKey(geminiPool)}
		// 先尝试复用本次响应内已经查询过的窗口统计。
		stats, ok := statsByWindow[key]
		// 没有缓存时才真正查询 repository。
		if !ok {
			// repository 内部会按窗口长度选择 raw group by 或 hourly rollup。
			var err error
			// 调用窗口统计查询，end 使用半开区间避免重复累计边界事件。
			if geminiPool != nil {
				// 池级 row 用 Gemini/第三方 LIKE 谓词，和 weekly 行同口径，兼容模型名变体。
				stats, err = statsProvider.SumByAuthIndexAndPoolGemini(ctx, authIndex, windowStart, &windowEnd, *geminiPool)
			} else {
				// 普通 row 统计全模型用量。
				stats, err = statsProvider.SumByAuthIndex(ctx, authIndex, windowStart, &windowEnd)
			}
			// 统计失败不影响 quota 主结果，只跳过当前窗口用量展示。
			if err != nil {
				// 当前 row 不写 token/cost，继续处理其它 row。
				continue
			}
			// 把查询结果放入本次响应缓存，供相同窗口的其它 row 复用。
			statsByWindow[key] = stats
		}
		// 复制 tokens/cost 值，确保指针指向当前 row 独立变量。
		tokens := stats.Tokens
		cost := stats.Cost
		// token 和 cost 必须来自同一统计口径；provider pair 不完整时整对使用本地统计。
		response.Quota[index].WindowUsageTokens = &tokens
		response.Quota[index].WindowUsageCost = &cost
	}
	// 返回已经补充窗口用量的 quota 响应。
	return response
}

func shouldBackfillWindowUsageStats(row QuotaRow) bool {
	// provider 已给完整窗口用量时直接信任上游，不再用 usage_events 覆盖。
	if row.WindowUsageTokens != nil && row.WindowUsageCost != nil {
		return false
	}
	// 本地 usage_events 兜底只适用于普通 5h/Weekly/Monthly window scope。
	if !strings.EqualFold(strings.TrimSpace(row.Scope), "window") {
		return false
	}
	if row.Window == nil || row.Window.Seconds == nil {
		return false
	}
	switch *row.Window.Seconds {
	case quotaWindowFiveHourSeconds, quotaWindowSevenDaySeconds, quotaWindowThirtyDaySeconds, quotaWindowAverageMonthSeconds:
		return true
	default:
		return false
	}
}

func antigravityPoolCacheKey(geminiPool *bool) string {
	// nil 表示不限制池，对应全量窗口统计。
	if geminiPool == nil {
		return ""
	}
	// 两个池映射成稳定标识，确保 Gemini 与第三方池命中不同缓存条目。
	if *geminiPool {
		return string(AntigravityPoolGemini)
	}
	return string(AntigravityPoolThirdParty)
}

// antigravityPoolUsageWindow returns the SQL window for a pool 5h row's token/cost backfill.
// It aligns the window to the bottleneck model's current cycle [resetAt-5h, now] so the cost
// shares the same cycle as the displayed used% (which comes from that bottleneck model). This
// keeps the estimate (cost / used-ratio) consistent: a fresh cycle showing 100% remaining will
// then report ~0 spend rather than dragging in the previous cycle.
//
// The alignment only applies when resetAt is a sane in-progress cycle boundary; otherwise it
// falls back to a fixed [now-5h, now] lookback. Falling back covers the cases the fixed window
// was introduced for: a missing/unparseable resetAt, a window start projected into the future,
// or a resetAt already in the past (the cycle just rolled over and the pool reads ~100%).
func antigravityPoolUsageWindow(row QuotaRow, now time.Time) (time.Time, time.Time, bool) {
	if row.Window == nil || row.Window.Seconds == nil || *row.Window.Seconds <= 0 {
		return time.Time{}, time.Time{}, false
	}
	now = timeutil.NormalizeStorageTime(now)
	fixedStart := now.Add(-time.Duration(*row.Window.Seconds) * time.Second)
	if row.ResetAt == "" {
		return fixedStart, now, true
	}
	resetAt, err := timeutil.ParseStorageTime(row.ResetAt)
	if err != nil {
		return fixedStart, now, true
	}
	resetAt = timeutil.NormalizeStorageTime(resetAt)
	// resetAt must be in the future (cycle still running) for an aligned window to make sense.
	if !now.Before(resetAt) {
		return fixedStart, now, true
	}
	windowStart := resetAt.Add(-time.Duration(*row.Window.Seconds) * time.Second)
	// A window start in the future (resetAt more than one window away) is stale data; fall back.
	if windowStart.After(now) {
		return fixedStart, now, true
	}
	return windowStart, now, true
}

func quotaRowUsageWindow(row QuotaRow, now time.Time) (time.Time, time.Time, bool) {
	// window.seconds 是计算窗口起点的必要条件，没有明确窗口长度就不能补 token/cost。
	if row.Window == nil || row.Window.Seconds == nil || *row.Window.Seconds <= 0 {
		// 返回 false 表示该 row 不参与窗口 token/cost 计算。
		return time.Time{}, time.Time{}, false
	}
	// now 归一化成项目存储时间，作为 reset_after 推导和当前未结束窗口的实际查询终点。
	now = timeutil.NormalizeStorageTime(now)
	// resetAt 承接 provider 返回的绝对 reset_at，或由 reset_after_seconds 推导出的绝对重置时间。
	var resetAt time.Time
	// reset_at 有值时优先使用绝对时间，避免本地 now 和上游响应时间之间的网络延迟影响窗口。
	if row.ResetAt != "" {
		// provider 返回的 reset_at 当前统一按 RFC3339 解析。
		parsedResetAt, err := timeutil.ParseStorageTime(row.ResetAt)
		// reset_at 解析失败时不能安全计算窗口，直接跳过。
		if err != nil {
			// 返回 false 表示该 row 不参与窗口 token/cost 计算。
			return time.Time{}, time.Time{}, false
		}
		// reset_at 归一化成项目存储时间，保证与 usage_events.timestamp 比较一致。
		resetAt = timeutil.NormalizeStorageTime(parsedResetAt)
	} else if row.ResetAfterSeconds != nil && *row.ResetAfterSeconds >= 0 {
		// 只有相对 reset_after_seconds 可用时，用当前刷新时间推导本轮 quota 的重置点。
		resetAt = now.Add(time.Duration(*row.ResetAfterSeconds) * time.Second)
	} else {
		// 既没有 reset_at 也没有 reset_after_seconds 时无法定位窗口结束点。
		return time.Time{}, time.Time{}, false
	}
	// windowStart 是 reset_at 往前减去明确窗口秒数。
	windowStart := resetAt.Add(-time.Duration(*row.Window.Seconds) * time.Second)
	// 当前时间早于 reset_at 时，窗口还没结束，查询终点用 now 避免扫描未来空小时。
	if now.Before(resetAt) {
		// 返回 [windowStart, now) 作为当前窗口统计范围。
		return windowStart, now, true
	}
	// 当前时间已经到达或超过 reset_at 时，查询终点用 reset_at 锁定旧窗口。
	return windowStart, resetAt, true
}
