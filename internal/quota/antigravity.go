package quota

import (
	"context"
	"fmt"

	"cpa-usage-keeper/internal/cpa/dto/apicall"
)

type antigravityProvider struct {
	caller         ManagementAPICaller
	configs        []APICallConfig
	summaryConfigs []APICallConfig
}

func NewAntigravityProvider(caller ManagementAPICaller, configs ...APICallConfig) ProviderHandler {
	return antigravityProvider{caller: caller, configs: configs}
}

// NewAntigravityProviderWithSummary builds a provider that also queries
// retrieveUserQuotaSummary (summaryConfigs) for live weekly buckets. A failed or empty
// summary call is non-fatal: the model-list result is still returned without weekly data.
func NewAntigravityProviderWithSummary(caller ManagementAPICaller, configs []APICallConfig, summaryConfigs []APICallConfig) ProviderHandler {
	return antigravityProvider{caller: caller, configs: configs, summaryConfigs: summaryConfigs}
}

func (p antigravityProvider) Check(ctx context.Context, input ProviderInput) (ProviderOutput, error) {
	// Antigravity quota 依赖 project_id；缺少时阻断请求并提示用户补齐认证文件元数据。
	if input.Identity.ProjectID == nil || *input.Identity.ProjectID == "" {
		return ProviderOutput{}, fmt.Errorf("%w: missing project_id parameter", ErrProviderInput)
	}
	if len(p.configs) == 0 {
		return ProviderOutput{}, fmt.Errorf("%w: antigravity config is required", ErrProviderInput)
	}
	// 多个候选 endpoint 按配置顺序尝试，直到解析到可用 quota 为止。
	var lastErr error
	for _, config := range p.configs {
		response, err := p.caller.CallManagementAPI(ctx, apicall.Request{
			AuthIndex: input.Identity.Identity,
			Method:    config.Method,
			URL:       config.URL,
			Header:    copyHeaders(config.Headers),
			Data:      map[string]string{"project": *input.Identity.ProjectID},
		})
		if err != nil {
			lastErr = err
			continue
		}
		quota, err := parseAntigravityQuotaPayload(response)
		if err != nil {
			lastErr = err
			continue
		}
		// 周额度桶来自独立的 retrieveUserQuotaSummary 接口；取不到时不影响模型级 5h 展示。
		weekly := p.fetchWeeklyBuckets(ctx, input)
		return ProviderOutput{Provider: "antigravity", Result: AntigravityResult{Quota: quota, WeeklyBuckets: weekly}}, nil
	}
	return ProviderOutput{}, lastErr
}

// fetchWeeklyBuckets queries retrieveUserQuotaSummary across the candidate endpoints and
// returns the first successfully parsed per-pool weekly buckets. It never returns an error:
// the weekly data is supplementary, so any failure simply yields nil and the caller falls
// back to the 429-based weekly estimate.
func (p antigravityProvider) fetchWeeklyBuckets(ctx context.Context, input ProviderInput) map[AntigravityPool]AntigravityWeeklyBucket {
	for _, config := range p.summaryConfigs {
		response, err := p.caller.CallManagementAPI(ctx, apicall.Request{
			AuthIndex: input.Identity.Identity,
			Method:    config.Method,
			URL:       config.URL,
			Header:    copyHeaders(config.Headers),
			Data:      map[string]string{"project": *input.Identity.ProjectID},
		})
		if err != nil {
			continue
		}
		buckets, err := parseAntigravityWeeklyBuckets(response)
		if err != nil || len(buckets) == 0 {
			continue
		}
		return buckets
	}
	return nil
}
