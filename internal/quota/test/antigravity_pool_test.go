package test

import (
	"testing"

	"cpa-usage-keeper/internal/quota"
)

func TestClassifyAntigravityPool(t *testing.T) {
	cases := []struct {
		model string
		want  quota.AntigravityPool
	}{
		{"gemini-3-pro-high", quota.AntigravityPoolGemini},
		{"gemini-3-flash", quota.AntigravityPoolGemini},
		{"gemini-3.1-flash-lite", quota.AntigravityPoolGemini},
		{"gemini-3.1-flash-image", quota.AntigravityPoolGemini},
		{"Gemini-3.1-Pro-Low", quota.AntigravityPoolGemini},
		{"claude-opus-4-6-thinking", quota.AntigravityPoolThirdParty},
		{"claude-sonnet-4-6", quota.AntigravityPoolThirdParty},
		{"gpt-oss-120b-medium", quota.AntigravityPoolThirdParty},
		{"", quota.AntigravityPoolThirdParty},
		{"  gemini-3-pro-high  ", quota.AntigravityPoolGemini},
	}
	for _, tc := range cases {
		if got := quota.ClassifyAntigravityPool(tc.model); got != tc.want {
			t.Fatalf("ClassifyAntigravityPool(%q) = %q, want %q", tc.model, got, tc.want)
		}
	}
}
