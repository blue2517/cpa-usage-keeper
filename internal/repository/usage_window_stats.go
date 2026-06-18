package repository

import (
	"context"
	"fmt"
	"strings"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/helper"
	"cpa-usage-keeper/internal/timeutil"

	"gorm.io/gorm"
)

const quotaWindowRawOnlyThreshold = 5 * time.Hour

type UsageWindowStats struct {
	Tokens int64
	Cost   float64
}

type UsageWindowStatsCalculator struct {
	db           *gorm.DB
	costResolver *UsageCostResolver
}

type usageWindowTokenStats struct {
	Model               string `gorm:"column:model"`
	ModelAlias          string `gorm:"column:model_alias"`
	TotalTokens         int64  `gorm:"column:total_tokens"`
	InputTokens         int64  `gorm:"column:input_tokens"`
	OutputTokens        int64  `gorm:"column:output_tokens"`
	CachedTokens        int64  `gorm:"column:cached_tokens"`
	CacheReadTokens     int64  `gorm:"column:cache_read_tokens"`
	CacheCreationTokens int64  `gorm:"column:cache_creation_tokens"`
}

type usageWindowTokenStatsKey struct {
	model      string
	modelAlias string
}

func NewUsageWindowStatsCalculator(ctx context.Context, db *gorm.DB) (*UsageWindowStatsCalculator, error) {
	if db == nil {
		return nil, fmt.Errorf("database is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	costResolver, err := NewUsageCostResolver(ctx, db)
	if err != nil {
		return nil, err
	}
	return &UsageWindowStatsCalculator{db: db, costResolver: costResolver}, nil
}

func (c *UsageWindowStatsCalculator) SumByAuthIndex(ctx context.Context, authIndex string, start time.Time, end *time.Time) (UsageWindowStats, error) {
	return c.SumByAuthIndexAndModels(ctx, authIndex, start, end, nil)
}

func (c *UsageWindowStatsCalculator) SumByAuthIndexAndModels(ctx context.Context, authIndex string, start time.Time, end *time.Time, models []string) (UsageWindowStats, error) {
	if c == nil || c.db == nil {
		return UsageWindowStats{}, fmt.Errorf("usage window stats calculator is nil")
	}
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" {
		return UsageWindowStats{}, fmt.Errorf("auth_index is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	modelFilter := normalizeModelFilter(models)
	rows, err := loadUsageWindowTokenStats(c.db.WithContext(ctx), authIndex, start, end, modelFilter)
	if err != nil {
		return UsageWindowStats{}, err
	}
	return usageWindowStatsFromTokenStats(rows, c.costResolver), nil
}

func (c *UsageWindowStatsCalculator) SumByAuthIndexAndPoolGemini(ctx context.Context, authIndex string, start time.Time, end *time.Time, geminiPool bool) (UsageWindowStats, error) {
	if c == nil || c.db == nil {
		return UsageWindowStats{}, fmt.Errorf("usage window stats calculator is nil")
	}
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" {
		return UsageWindowStats{}, fmt.Errorf("auth_index is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	rows, err := loadUsageWindowTokenStatsWithPool(c.db.WithContext(ctx), authIndex, start, end, &geminiPool)
	if err != nil {
		return UsageWindowStats{}, err
	}
	return usageWindowStatsFromTokenStats(rows, c.costResolver), nil
}

func SumUsageWindowStatsByAuthIndex(ctx context.Context, db *gorm.DB, authIndex string, start time.Time, end *time.Time) (UsageWindowStats, error) {
	// 无模型过滤时复用带过滤版本，保持旧调用方语义不变。
	return SumUsageWindowStatsByAuthIndexAndModels(ctx, db, authIndex, start, end, nil)
}

// SumUsageWindowStatsByAuthIndexAndModels mirrors SumUsageWindowStatsByAuthIndex but
// optionally restricts the aggregation to a set of models. An empty models slice
// means no model restriction. Antigravity pool rows use this to count only one
// pool's models (Gemini vs third-party) within a 5h/weekly window.
func SumUsageWindowStatsByAuthIndexAndModels(ctx context.Context, db *gorm.DB, authIndex string, start time.Time, end *time.Time, models []string) (UsageWindowStats, error) {
	calculator, err := NewUsageWindowStatsCalculator(ctx, db)
	if err != nil {
		return UsageWindowStats{}, err
	}
	return calculator.SumByAuthIndexAndModels(ctx, authIndex, start, end, models)
}

// AntigravityPoolModelLike is the SQL LIKE pattern that matches Gemini-pool models.
// Models matching it belong to the Gemini pool; the rest belong to the third-party pool.
const AntigravityPoolModelLike = "%gemini%"

// SumUsageWindowStatsByAuthIndexAndPoolGemini sums window token/cost for one auth,
// restricted to either the Gemini pool (geminiPool=true) or the third-party pool
// (geminiPool=false). Antigravity weekly-cap accumulation uses this because the
// detector only knows the pool classification rule, not an explicit model list.
func SumUsageWindowStatsByAuthIndexAndPoolGemini(ctx context.Context, db *gorm.DB, authIndex string, start time.Time, end *time.Time, geminiPool bool) (UsageWindowStats, error) {
	calculator, err := NewUsageWindowStatsCalculator(ctx, db)
	if err != nil {
		return UsageWindowStats{}, err
	}
	return calculator.SumByAuthIndexAndPoolGemini(ctx, authIndex, start, end, geminiPool)
}

func loadUsageWindowTokenStats(db *gorm.DB, authIndex string, start time.Time, end *time.Time, models []string) ([]usageWindowTokenStats, error) {
	return loadUsageWindowTokenStatsFiltered(db, authIndex, start, end, modelWindowFilter{models: models})
}

func loadUsageWindowTokenStatsWithPool(db *gorm.DB, authIndex string, start time.Time, end *time.Time, geminiPool *bool) ([]usageWindowTokenStats, error) {
	return loadUsageWindowTokenStatsFiltered(db, authIndex, start, end, modelWindowFilter{geminiPool: geminiPool})
}

// modelWindowFilter carries an optional model restriction for window aggregation:
// either an explicit model set or an Antigravity Gemini/third-party pool predicate.
type modelWindowFilter struct {
	models     []string
	geminiPool *bool
}

func applyModelWindowFilter(query *gorm.DB, filter modelWindowFilter) *gorm.DB {
	if len(filter.models) > 0 {
		query = query.Where("model IN ?", filter.models)
	}
	if filter.geminiPool != nil {
		if *filter.geminiPool {
			query = query.Where("LOWER(model) LIKE ?", AntigravityPoolModelLike)
		} else {
			query = query.Where("LOWER(model) NOT LIKE ?", AntigravityPoolModelLike)
		}
	}
	return query
}

func loadUsageWindowTokenStatsFiltered(db *gorm.DB, authIndex string, start time.Time, end *time.Time, filter modelWindowFilter) ([]usageWindowTokenStats, error) {
	// 空时间无法表达有效 quota 窗口，提前返回避免误构造超宽时间范围。
	if start.IsZero() || (end != nil && end.IsZero()) {
		// 返回空结果而不是错误，调用方会把它当作“该窗口暂无用量”。
		return nil, nil
	}
	// 没有结束时间时只能走 raw 查询，保持”从 start 到当前已有数据”的旧语义。
	if end == nil {
		return sumRawUsageWindowTokenStats(db, authIndex, start, nil, filter)
	}
	// 结束时间归一化为存储时区，避免和 SQLite 文本时间比较口径不一致。
	windowEnd := timeutil.NormalizeStorageTime(*end)
	// 开始时间归一化为存储时区，确保后续整点切分与查询参数一致。
	windowStart := timeutil.NormalizeStorageTime(start)
	// 空窗口或反向窗口没有统计意义，直接返回空结果。
	if !windowStart.Before(windowEnd) {
		// 返回空 slice 而不是错误，方便调用方统一累加。
		return nil, nil
	}
	// 5 小时及以内直接查 raw，避免小窗口为了 rollup 多打几次数据库。
	if windowEnd.Sub(windowStart) <= quotaWindowRawOnlyThreshold {
		return sumRawUsageWindowTokenStats(db, authIndex, windowStart, &windowEnd, filter)
	}
	// 长窗口拆成边界 raw 和中间完整小时 rollup，降低真实高频数据下的扫描行数。
	return sumLongUsageWindowTokenStats(db, authIndex, windowStart, windowEnd, filter)
}

func sumLongUsageWindowTokenStats(db *gorm.DB, authIndex string, start time.Time, end time.Time, filter modelWindowFilter) ([]usageWindowTokenStats, error) {
	// 左边界结束点取 start 之后的第一个整点，只有非整点部分才需要 raw 补偿。
	leftEnd := ceilUsageWindowHour(start)
	// 如果窗口不到左边界整点就结束，左边界最多只能补到 end。
	if end.Before(leftEnd) {
		// 把左边界裁剪到实际窗口结束，避免 raw 查询越过窗口。
		leftEnd = end
	}
	// 右边界开始点取 end 所在整点，整点之后的尾巴需要 raw 补偿。
	rightStart := end.Truncate(time.Hour)
	// 最近一个完整小时可能刚写入 usage_events 但还没进入 hourly rollup，所以再向前保守一小时。
	safeHourlyEnd := rightStart.Add(-time.Hour)
	// 如果保守后的 hourly 结束点比原右边界更早，就扩大右边界 raw 覆盖范围。
	if safeHourlyEnd.Before(rightStart) {
		// raw 右边界覆盖最近一到两小时，换取对聚合滞后的稳定兼容。
		rightStart = safeHourlyEnd
	}
	// 如果右边界起点落在窗口开始之前，说明没有完整小时可用。
	if rightStart.Before(start) {
		// 把右边界起点裁剪到 start，避免读取窗口外数据。
		rightStart = start
	}
	if rightStart.Before(leftEnd) {
		// 右边界不能早于左边界结束点，否则两个 raw 半开区间会重叠并重复计数。
		rightStart = leftEnd
	}
	// 完整小时开始于左边界之后的整点。
	hourlyStart := leftEnd
	// 完整小时结束于保守后的右边界起点。
	hourlyEnd := rightStart
	// 用 map 按 model_alias/model 合并 left raw、hourly、right raw 的结果。
	merged := make(map[usageWindowTokenStatsKey]usageWindowTokenStats)
	// 左边界存在时读取 usage_events 边界段。
	if start.Before(leftEnd) {
		// 查询左边界 raw 聚合，最多覆盖不足一小时的数据。
		rows, err := sumRawUsageWindowTokenStats(db, authIndex, start, &leftEnd, filter)
		// 左边界查询失败时直接返回，避免展示半截统计。
		if err != nil {
			// 包装左边界错误，便于测试和日志定位。
			return nil, fmt.Errorf("sum left raw usage window stats: %w", err)
		}
		// 把左边界 model 聚合结果合并到总结果。
		mergeUsageWindowTokenStats(merged, rows)
	}
	// 中间存在完整小时时读取 hourly rollup。
	if hourlyStart.Before(hourlyEnd) {
		// 查询完整小时 rollup 聚合，避免扫描 7 天 raw events。
		rows, err := sumHourlyUsageWindowTokenStats(db, authIndex, hourlyStart, hourlyEnd, filter)
		// hourly 查询失败时直接返回，避免展示半截统计。
		if err != nil {
			// 包装 hourly 错误，便于区分 raw 和 rollup 问题。
			return nil, fmt.Errorf("sum hourly usage window stats: %w", err)
		}
		// 把 hourly model 聚合结果合并到总结果。
		mergeUsageWindowTokenStats(merged, rows)
	}
	// 右边界存在时读取 usage_events 尾部段。
	if rightStart.Before(end) {
		// 查询右边界 raw 聚合，覆盖最后一个完整小时之后的数据。
		rows, err := sumRawUsageWindowTokenStats(db, authIndex, rightStart, &end, filter)
		// 右边界查询失败时直接返回，避免展示半截统计。
		if err != nil {
			// 包装右边界错误，便于测试和日志定位。
			return nil, fmt.Errorf("sum right raw usage window stats: %w", err)
		}
		// 把右边界 model 聚合结果合并到总结果。
		mergeUsageWindowTokenStats(merged, rows)
	}
	// 把 map 转回 slice，交给 cost 计算函数按 model 价格处理。
	return usageWindowTokenStatsValues(merged), nil
}

func sumRawUsageWindowTokenStats(db *gorm.DB, authIndex string, start time.Time, end *time.Time, filter modelWindowFilter) ([]usageWindowTokenStats, error) {
	// raw 查询只取 model_alias/model 级汇总字段，避免把大量 usage_events 行读进 Go 内存。
	query := db.Model(&entities.UsageEvent{}).
		// SELECT 中只聚合 token/cost 需要的字段，不读取 raw_json 等大字段。
		Select("model, COALESCE(model_alias, '') AS model_alias, COALESCE(SUM(total_tokens), 0) AS total_tokens, COALESCE(SUM(input_tokens), 0) AS input_tokens, COALESCE(SUM(output_tokens), 0) AS output_tokens, COALESCE(SUM(cached_tokens), 0) AS cached_tokens, COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens, COALESCE(SUM(cache_creation_tokens), 0) AS cache_creation_tokens").
		// auth_index 已经是唯一身份维度，这里不再额外按 auth_type 过滤。
		Where("auth_index = ? AND timestamp >= ?", authIndex, timeutil.FormatStorageTime(start)).
		// 按 model_alias/model 分组保留现有聚合边界，后续 cost 先按真实 model、再按 alias 回退。
		Group("model_alias, model")
	// 如果调用方传入结束时间，就用半开区间避免边界重复累计。
	if end != nil {
		query = query.Where("timestamp < ?", timeutil.FormatStorageTime(*end))
	}
	query = applyModelWindowFilter(query, filter)
	var rows []usageWindowTokenStats
	if err := query.Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("sum raw usage window stats: %w", err)
	}
	// 返回 model_alias/model 级 token 汇总。
	return rows, nil
}

func sumHourlyUsageWindowTokenStats(db *gorm.DB, authIndex string, start time.Time, end time.Time, filter modelWindowFilter) ([]usageWindowTokenStats, error) {
	query := db.Model(&entities.UsageOverviewHourlyStat{}).
		// SELECT 中聚合 token/cost 需要的字段，保持和 raw 查询返回结构一致。
		Select("model, model_alias, COALESCE(SUM(total_tokens), 0) AS total_tokens, COALESCE(SUM(input_tokens), 0) AS input_tokens, COALESCE(SUM(output_tokens), 0) AS output_tokens, COALESCE(SUM(cached_tokens), 0) AS cached_tokens, COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens, COALESCE(SUM(cache_creation_tokens), 0) AS cache_creation_tokens").
		// auth_index + bucket_start 范围可以使用现有 hourly auth_bucket 索引。
		Where("auth_index = ? AND bucket_start >= ? AND bucket_start < ?", authIndex, timeutil.FormatStorageTime(start), timeutil.FormatStorageTime(end)).
		// 按 model_alias/model 分组保留现有聚合边界，后续 cost 先按真实 model、再按 alias 回退。
		Group("model_alias, model")
	query = applyModelWindowFilter(query, filter)
	var rows []usageWindowTokenStats
	if err := query.Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("sum hourly usage window stats: %w", err)
	}
	// 返回 model_alias/model 级 token 汇总。
	return rows, nil
}

func mergeUsageWindowTokenStats(merged map[usageWindowTokenStatsKey]usageWindowTokenStats, rows []usageWindowTokenStats) {
	// 遍历每个来源返回的 model_alias/model 汇总行。
	for _, row := range rows {
		// 维度先 trim，避免同一模型因为空白产生两个聚合桶。
		model := strings.TrimSpace(row.Model)
		modelAlias := strings.TrimSpace(row.ModelAlias)
		key := usageWindowTokenStatsKey{model: model, modelAlias: modelAlias}
		// 从已有 map 中取出当前 model_alias/model 的累计值。
		current := merged[key]
		// 写回规范化后的维度，后续 resolver 也按 trim 后的值查找。
		current.Model = model
		current.ModelAlias = modelAlias
		// 累加 total_tokens，用于前端 token 展示。
		current.TotalTokens += row.TotalTokens
		// 累加 input_tokens，用于 prompt/cached 成本拆分。
		current.InputTokens += row.InputTokens
		// 累加 output_tokens，用于 completion 成本计算。
		current.OutputTokens += row.OutputTokens
		// 累加 cached_tokens，用于 cache 成本计算并从 prompt 中扣除。
		current.CachedTokens += row.CachedTokens
		// 累加 Claude cache read 明细，用于 Claude 风格价格计算。
		current.CacheReadTokens += row.CacheReadTokens
		// 累加 Claude cache creation/write 明细，用于 Claude 风格价格计算。
		current.CacheCreationTokens += row.CacheCreationTokens
		// 把合并后的 model_alias/model 统计写回 map。
		merged[key] = current
	}
}

func usageWindowTokenStatsValues(merged map[usageWindowTokenStatsKey]usageWindowTokenStats) []usageWindowTokenStats {
	// 预分配 slice 容量，避免 model 数较多时反复扩容。
	rows := make([]usageWindowTokenStats, 0, len(merged))
	// 遍历 map 中已经合并好的 model 统计。
	for _, row := range merged {
		// 把单个 model 的累计统计追加到返回列表。
		rows = append(rows, row)
	}
	// 返回列表顺序不影响最终 token/cost 汇总。
	return rows
}

func usageWindowStatsFromTokenStats(rows []usageWindowTokenStats, costResolver *UsageCostResolver) UsageWindowStats {
	// 初始化最终返回的 token/cost 汇总。
	stats := UsageWindowStats{}
	// 遍历每个 model_alias/model 的聚合 token。
	for _, row := range rows {
		// total_tokens 直接累计到前端展示的窗口 token。
		stats.Tokens += row.TotalTokens
		// 使用统一 resolver 先按真实 model、再按 alias 回退计算该维度的 cost。
		result := costResolver.Calculate(UsageCostSubject{
			Model:      row.Model,
			ModelAlias: row.ModelAlias,
			Tokens: helper.UsageTokenCostInput{
				InputTokens:         row.InputTokens,
				OutputTokens:        row.OutputTokens,
				CachedTokens:        row.CachedTokens,
				CacheReadTokens:     row.CacheReadTokens,
				CacheCreationTokens: row.CacheCreationTokens,
			},
		})
		stats.Cost += result.Cost.TotalCostUSD
	}
	// 返回最终窗口统计。
	return stats
}

func normalizeModelFilter(models []string) []string {
	// 空输入直接返回 nil，下游按“不限制模型”处理。
	if len(models) == 0 {
		return nil
	}
	// 用 set 去重，避免同一 model 多次出现导致 IN 列表冗长。
	seen := make(map[string]struct{}, len(models))
	filter := make([]string, 0, len(models))
	for _, model := range models {
		trimmed := strings.TrimSpace(model)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		filter = append(filter, trimmed)
	}
	// 全是空白时退化为不限制模型。
	if len(filter) == 0 {
		return nil
	}
	return filter
}

func ceilUsageWindowHour(value time.Time) time.Time {
	// 先把时间归一化，避免不同 location 下 Truncate 结果难以比较。
	value = timeutil.NormalizeStorageTime(value)
	// 取当前时间所在小时的整点。
	truncated := value.Truncate(time.Hour)
	// 如果本身已经是整点，就直接返回当前整点。
	if value.Equal(truncated) {
		// 整点窗口不需要左边界 raw 补偿。
		return truncated
	}
	// 非整点时返回下一个整点，作为完整小时 rollup 的开始。
	return truncated.Add(time.Hour)
}
