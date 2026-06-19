package test

import (
	"testing"

	"cpa-usage-keeper/internal/quota"
)

func TestIsAntigravityWeeklyExhaustion(t *testing.T) {
	cases := []struct {
		name string
		e    quota.AntigravityWeeklyExhaustionEvent
		want bool
	}{
		{
			name: "quota exhausted 429 without retry delay is weekly",
			e: quota.AntigravityWeeklyExhaustionEvent{
				ExecutorType:   "AntigravityExecutor",
				FailStatusCode: 429,
				FailBody:       `{"error":{"status":"RESOURCE_EXHAUSTED","message":"QUOTA_EXHAUSTED"}}`,
			},
			want: true,
		},
		{
			name: "quota exhausted in error details without retry delay is weekly",
			e: quota.AntigravityWeeklyExhaustionEvent{
				ExecutorType:   "AntigravityExecutor",
				FailStatusCode: 429,
				FailBody:       `{"error":{"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED"}]}}`,
			},
			want: true,
		},
		{
			name: "quota exhausted with short retryDelay is 5h exhaustion, not weekly",
			e: quota.AntigravityWeeklyExhaustionEvent{
				ExecutorType:   "AntigravityExecutor",
				FailStatusCode: 429,
				FailBody: `{"error":{"status":"RESOURCE_EXHAUSTED","details":[
					{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED"},
					{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"3600s"}
				]}}`,
			},
			want: false,
		},
		{
			name: "quota exhausted with retryDelay over 5h is weekly",
			e: quota.AntigravityWeeklyExhaustionEvent{
				ExecutorType:   "AntigravityExecutor",
				FailStatusCode: 429,
				FailBody: `{"error":{"status":"RESOURCE_EXHAUSTED","details":[
					{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED"},
					{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"518400s"}
				]}}`,
			},
			want: true,
		},
		{
			name: "quota exhausted with quotaResetDelay under 5h is 5h exhaustion",
			e: quota.AntigravityWeeklyExhaustionEvent{
				ExecutorType:   "AntigravityExecutor",
				FailStatusCode: 429,
				FailBody: `{"error":{"status":"RESOURCE_EXHAUSTED","details":[
					{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED","metadata":{"quotaResetDelay":"479.417207ms"}}
				]}}`,
			},
			want: false,
		},
		{
			name: "quota exhausted with human readable reset under 5h is 5h exhaustion",
			e: quota.AntigravityWeeklyExhaustionEvent{
				ExecutorType:   "AntigravityExecutor",
				FailStatusCode: 429,
				FailBody:       `{"error":{"status":"RESOURCE_EXHAUSTED","message":"QUOTA_EXHAUSTED. Your quota will reset after 1h43m56s."}}`,
			},
			want: false,
		},
		{
			name: "quota exhausted with human readable reset over 5h is weekly",
			e: quota.AntigravityWeeklyExhaustionEvent{
				ExecutorType:   "AntigravityExecutor",
				FailStatusCode: 429,
				FailBody:       `{"error":{"status":"RESOURCE_EXHAUSTED","message":"QUOTA_EXHAUSTED. Your quota will reset after 120h0m0s."}}`,
			},
			want: true,
		},
		{
			name: "retryDelay exactly 5h is still 5h exhaustion",
			e: quota.AntigravityWeeklyExhaustionEvent{
				ExecutorType:   "AntigravityExecutor",
				FailStatusCode: 429,
				FailBody: `{"error":{"status":"RESOURCE_EXHAUSTED","details":[
					{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED"},
					{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"18000s"}
				]}}`,
			},
			want: false,
		},
		{
			name: "rate limit exceeded is not weekly exhaustion",
			e: quota.AntigravityWeeklyExhaustionEvent{
				ExecutorType:   "AntigravityExecutor",
				FailStatusCode: 429,
				FailBody:       `{"error":{"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED"}]}}`,
			},
			want: false,
		},
		{
			name: "non-429 status code",
			e: quota.AntigravityWeeklyExhaustionEvent{
				ExecutorType:   "AntigravityExecutor",
				FailStatusCode: 500,
				FailBody:       `QUOTA_EXHAUSTED`,
			},
			want: false,
		},
		{
			name: "non-antigravity executor",
			e: quota.AntigravityWeeklyExhaustionEvent{
				ExecutorType:   "GeminiExecutor",
				FailStatusCode: 429,
				FailBody:       `QUOTA_EXHAUSTED`,
			},
			want: false,
		},
		{
			name: "empty executor type",
			e: quota.AntigravityWeeklyExhaustionEvent{
				FailStatusCode: 429,
				FailBody:       `QUOTA_EXHAUSTED`,
			},
			want: false,
		},
		{
			name: "empty body",
			e: quota.AntigravityWeeklyExhaustionEvent{
				ExecutorType:   "AntigravityExecutor",
				FailStatusCode: 429,
				FailBody:       "",
			},
			want: false,
		},
		{
			name: "zero status code",
			e: quota.AntigravityWeeklyExhaustionEvent{
				ExecutorType: "AntigravityExecutor",
				FailBody:     `QUOTA_EXHAUSTED`,
			},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := quota.IsAntigravityWeeklyExhaustion(tc.e); got != tc.want {
				t.Fatalf("IsAntigravityWeeklyExhaustion() = %v, want %v", got, tc.want)
			}
		})
	}
}
