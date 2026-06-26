package test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"cpa-usage-keeper/internal/cpa/dto/apicall"
	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/quota"
)

func TestAntigravityProviderUsesProjectIDForQuotaRequest(t *testing.T) {
	caller := &recordingManagementCaller{responses: []*apicall.Response{{
		StatusCode: 200,
		BodyText:   `{"models":{"pro":{"displayName":"Pro","quotaInfo":{"remainingFraction":0.4,"remaining":12,"resetTime":"2026-05-09T12:00:00Z"}},"flash":{"displayName":"Flash","quotaInfo":{"remainingFraction":0.9,"remaining":32,"resetTime":"2026-05-10T12:00:00Z"}}}}`,
		Body:       json.RawMessage(`{"models":{"pro":{"displayName":"Pro","quotaInfo":{"remainingFraction":0.4,"remaining":12,"resetTime":"2026-05-09T12:00:00Z"}},"flash":{"displayName":"Flash","quotaInfo":{"remainingFraction":0.9,"remaining":32,"resetTime":"2026-05-10T12:00:00Z"}}}}`),
	}}}
	provider := quota.NewAntigravityProvider(caller, quota.DefaultProviderConfigs().Antigravity[0])

	output, err := provider.Check(context.Background(), quota.ProviderInput{Identity: entities.UsageIdentity{
		Identity:  "ag-auth",
		ProjectID: stringPtr("project-123"),
	}})
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	if output.Provider != "antigravity" {
		t.Fatalf("expected antigravity output provider, got %q", output.Provider)
	}
	result, ok := output.Result.(quota.AntigravityResult)
	if !ok {
		t.Fatalf("expected antigravity result type, got %T", output.Result)
	}
	if result.Quota == nil || result.Quota.Models["pro"].DisplayName != "Pro" || result.Quota.Models["pro"].QuotaInfo.RemainingFraction != 0.4 || result.Quota.Models["pro"].QuotaInfo.Remaining != 12 || result.Quota.Models["flash"].DisplayName != "Flash" {
		t.Fatalf("expected parsed antigravity quota payload, got %#v", result.Quota)
	}
	encoded, err := json.Marshal(output.Result)
	if err != nil {
		t.Fatalf("marshal antigravity result: %v", err)
	}
	body := string(encoded)
	if !contains(body, `"models":{`) || !contains(body, `"pro":{"displayName":"Pro"`) || !contains(body, `"flash":{"displayName":"Flash"`) || contains(body, "bodyText") || contains(body, "statusCode") {
		t.Fatalf("unexpected antigravity result JSON: %s", body)
	}
	if len(caller.requests) != 1 {
		t.Fatalf("expected one api-call request, got %d", len(caller.requests))
	}
	request := caller.requests[0]
	if request.AuthIndex != "ag-auth" || request.Method != "POST" || request.URL != "https://daily-cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels" {
		t.Fatalf("unexpected api-call request: %+v", request)
	}
	if request.Header["Authorization"] != "Bearer $TOKEN$" || request.Header["Content-Type"] != "application/json" || request.Header["User-Agent"] != "antigravity/1.11.5 windows/amd64" {
		t.Fatalf("unexpected api-call headers: %+v", request.Header)
	}
	data, ok := request.Data.(map[string]string)
	if !ok || data["project"] != "project-123" {
		t.Fatalf("unexpected api-call data: %#v", request.Data)
	}
}

func TestAntigravityProviderParsesLiveWeeklyBuckets(t *testing.T) {
	modelsBody := `{"models":{"gemini-3-flash":{"displayName":"Gemini 3 Flash","quotaInfo":{"remainingFraction":1,"resetTime":"2026-06-27T02:29:00Z"}}}}`
	summaryBody := `{"groups":[` +
		`{"displayName":"Gemini 模型","buckets":[` +
		`{"window":"5h","remainingFraction":1,"resetTime":"2026-06-27T02:29:00Z"},` +
		`{"window":"weekly","remainingFraction":0.96,"resetTime":"2026-07-01T12:00:00Z"}]},` +
		`{"displayName":"Claude 和 GPT 模型","buckets":[` +
		`{"window":"5h","remainingFraction":1,"resetTime":"2026-06-27T02:29:00Z"},` +
		`{"window":"weekly","remainingFraction":0.91,"resetTime":"2026-07-03T16:00:00Z"}]}]}`
	caller := &recordingManagementCaller{responses: []*apicall.Response{
		{StatusCode: 200, BodyText: modelsBody, Body: json.RawMessage(modelsBody)},
		{StatusCode: 200, BodyText: summaryBody, Body: json.RawMessage(summaryBody)},
	}}
	configs := quota.DefaultProviderConfigs()
	provider := quota.NewAntigravityProviderWithSummary(caller, configs.Antigravity[:1], configs.AntigravityQuotaSummary[:1])

	output, err := provider.Check(context.Background(), quota.ProviderInput{Identity: entities.UsageIdentity{
		Identity:  "ag-auth",
		ProjectID: stringPtr("project-123"),
	}})
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	result, ok := output.Result.(quota.AntigravityResult)
	if !ok {
		t.Fatalf("expected antigravity result type, got %T", output.Result)
	}
	if len(result.WeeklyBuckets) != 2 {
		t.Fatalf("expected two weekly buckets, got %#v", result.WeeklyBuckets)
	}
	gemini := result.WeeklyBuckets[quota.AntigravityPoolGemini]
	if gemini.RemainingFraction != 0.96 || gemini.ResetTime != "2026-07-01T12:00:00Z" {
		t.Fatalf("unexpected gemini weekly bucket: %#v", gemini)
	}
	thirdParty := result.WeeklyBuckets[quota.AntigravityPoolThirdParty]
	if thirdParty.RemainingFraction != 0.91 || thirdParty.ResetTime != "2026-07-03T16:00:00Z" {
		t.Fatalf("unexpected third-party weekly bucket: %#v", thirdParty)
	}
	if len(caller.requests) != 2 {
		t.Fatalf("expected model-list + summary requests, got %d", len(caller.requests))
	}
	if caller.requests[1].URL != "https://daily-cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary" {
		t.Fatalf("unexpected summary request URL: %s", caller.requests[1].URL)
	}
}

func TestAntigravityProviderToleratesMissingSummary(t *testing.T) {
	modelsBody := `{"models":{"gemini-3-flash":{"displayName":"Gemini 3 Flash","quotaInfo":{"remainingFraction":1,"resetTime":"2026-06-27T02:29:00Z"}}}}`
	caller := &recordingManagementCaller{responses: []*apicall.Response{
		{StatusCode: 200, BodyText: modelsBody, Body: json.RawMessage(modelsBody)},
		{StatusCode: 500, BodyText: `{"error":"summary unavailable"}`, Body: json.RawMessage(`{"error":"summary unavailable"}`)},
	}}
	configs := quota.DefaultProviderConfigs()
	provider := quota.NewAntigravityProviderWithSummary(caller, configs.Antigravity[:1], configs.AntigravityQuotaSummary[:1])

	output, err := provider.Check(context.Background(), quota.ProviderInput{Identity: entities.UsageIdentity{
		Identity:  "ag-auth",
		ProjectID: stringPtr("project-123"),
	}})
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	result, ok := output.Result.(quota.AntigravityResult)
	if !ok {
		t.Fatalf("expected antigravity result type, got %T", output.Result)
	}
	if result.Quota == nil || result.Quota.Models["gemini-3-flash"].DisplayName != "Gemini 3 Flash" {
		t.Fatalf("expected model list to survive summary failure, got %#v", result.Quota)
	}
	if result.WeeklyBuckets != nil {
		t.Fatalf("expected nil weekly buckets on summary failure, got %#v", result.WeeklyBuckets)
	}
}

func TestAntigravityProviderRejectsMissingProjectID(t *testing.T) {
	caller := &recordingManagementCaller{}
	provider := quota.NewAntigravityProvider(caller, quota.DefaultProviderConfigs().Antigravity[0])

	_, err := provider.Check(context.Background(), quota.ProviderInput{Identity: entities.UsageIdentity{Identity: "ag-auth"}})
	if !errors.Is(err, quota.ErrProviderInput) || !strings.Contains(err.Error(), "missing project_id parameter") {
		t.Fatalf("expected missing project_id provider input error, got %v", err)
	}
	if len(caller.requests) != 0 {
		t.Fatalf("provider should not call api-call without project_id, got %d requests", len(caller.requests))
	}
}

func TestAntigravityProviderReturnsTargetErrorMessage(t *testing.T) {
	caller := &recordingManagementCaller{responses: []*apicall.Response{{
		StatusCode: 500,
		BodyText:   `{"error":"backend unavailable"}`,
		Body:       json.RawMessage(`{"error":"backend unavailable"}`),
	}}}
	provider := quota.NewAntigravityProvider(caller, quota.DefaultProviderConfigs().Antigravity[0])

	_, err := provider.Check(context.Background(), quota.ProviderInput{Identity: entities.UsageIdentity{
		Identity:  "ag-auth",
		ProjectID: stringPtr("project-123"),
	}})
	if err == nil || err.Error() != "HTTP 500: backend unavailable" {
		t.Fatalf("expected target HTTP message, got %v", err)
	}
}
