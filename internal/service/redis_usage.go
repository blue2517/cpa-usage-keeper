package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository/dto"
	"cpa-usage-keeper/internal/timeutil"
)

// DecodeRedisUsageMessage 将 redis_inboxes.raw_message 原样解码为 usage_events 入库实体。
func DecodeRedisUsageMessage(message string, fetchedAt time.Time) (entities.UsageEvent, json.RawMessage, error) {
	event, _, raw, err := DecodeRedisUsageMessageWithFail(message, fetchedAt)
	return event, raw, err
}

// DecodeRedisUsageMessageWithFail 在解码 usage_events 的同时返回网关上报的失败详情。
// 失败详情仅用于 ingestion 期间的瞬时判定（如 Antigravity 周限触顶检测），不落库。
func DecodeRedisUsageMessageWithFail(message string, fetchedAt time.Time) (entities.UsageEvent, queuedUsageFail, json.RawMessage, error) {
	raw := json.RawMessage(message)
	var payload queuedUsageDetail
	if err := json.Unmarshal(raw, &payload); err != nil {
		return entities.UsageEvent{}, queuedUsageFail{}, nil, fmt.Errorf("decode redis usage message: %w", err)
	}
	if strings.TrimSpace(payload.RequestID) == "" {
		return entities.UsageEvent{}, queuedUsageFail{}, raw, fmt.Errorf("decode redis usage message: request_id is required")
	}
	return payload.toUsageEvent(fetchedAt), payload.Fail, raw, nil
}

// queuedUsageDetail 对应 CPA Redis 队列中的单条 usage JSON payload。
type queuedUsageDetail struct {
	Timestamp       time.Time      `json:"timestamp"`
	LatencyMS       int64          `json:"latency_ms"`
	TTFTMS          *int64         `json:"ttft_ms"`
	Source          string         `json:"source"`
	AuthIndex       string         `json:"auth_index"`
	Tokens          dto.TokenStats `json:"tokens"`
	Failed          bool           `json:"failed"`
	Provider        string         `json:"provider"`
	Model           string         `json:"model"`
	Alias           *string        `json:"alias"`
	ReasoningEffort string         `json:"reasoning_effort"`
	ServiceTier     string         `json:"service_tier"`
	ExecutorType    string         `json:"executor_type"`
	Endpoint        string         `json:"endpoint"`
	AuthType        string         `json:"auth_type"`
	APIKey          string         `json:"api_key"`
	RequestID       string         `json:"request_id"`
	Fail            queuedUsageFail `json:"fail"`
}

// queuedUsageFail mirrors the gateway's per-request failure detail. It is only used
// transiently during ingestion (e.g. Antigravity weekly-cap detection) and is not
// persisted to usage_events.
type queuedUsageFail struct {
	StatusCode int    `json:"status_code"`
	Body       string `json:"body"`
}

func normalizeRedisAuthType(value string) string {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	if trimmed == "api_key" {
		return "apikey"
	}
	return trimmed
}

func trimRedisOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

// toUsageEvent 保持 Redis payload 的 model/request_id 语义，缺失时间才用本地拉取时间兜底。
func (d queuedUsageDetail) toUsageEvent(fetchedAt time.Time) entities.UsageEvent {
	apiGroupKey := firstNonEmpty(d.APIKey, d.Provider, d.Endpoint, "unknown")
	model := firstNonEmpty(d.Model, "unknown")
	timestamp := timeutil.NormalizeStorageTime(d.Timestamp)
	if timestamp.IsZero() {
		timestamp = timeutil.NormalizeStorageTime(fetchedAt)
	}
	source := strings.TrimSpace(d.Source)
	authIndex := strings.TrimSpace(d.AuthIndex)
	eventKey := strings.TrimSpace(d.RequestID)
	return entities.UsageEvent{
		EventKey:            eventKey,
		APIGroupKey:         apiGroupKey,
		Provider:            strings.TrimSpace(d.Provider),
		Endpoint:            strings.TrimSpace(d.Endpoint),
		AuthType:            normalizeRedisAuthType(d.AuthType),
		RequestID:           strings.TrimSpace(d.RequestID),
		Model:               model,
		ModelAlias:          trimRedisOptionalString(d.Alias),
		ReasoningEffort:     strings.TrimSpace(d.ReasoningEffort),
		ServiceTier:         strings.TrimSpace(d.ServiceTier),
		ExecutorType:        strings.TrimSpace(d.ExecutorType),
		Timestamp:           timestamp,
		Source:              source,
		AuthIndex:           authIndex,
		Failed:              d.Failed,
		LatencyMS:           max(d.LatencyMS, 0),
		TTFTMS:              d.TTFTMS,
		InputTokens:         d.Tokens.InputTokens,
		OutputTokens:        d.Tokens.OutputTokens,
		ReasoningTokens:     d.Tokens.ReasoningTokens,
		CachedTokens:        d.Tokens.CachedTokens,
		CacheReadTokens:     d.Tokens.CacheReadTokens,
		CacheCreationTokens: d.Tokens.CacheCreationTokens,
		TotalTokens:         d.Tokens.TotalTokens,
	}
}
