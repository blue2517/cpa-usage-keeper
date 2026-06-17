package quota

import "strings"

// AntigravityPool identifies one of Antigravity's two independent 5h/weekly quota pools.
// Antigravity meters Gemini models and third-party models (Claude/GPT/OSS) separately.
type AntigravityPool string

const (
	// AntigravityPoolGemini covers all gemini-* models, which share one quota pool.
	AntigravityPoolGemini AntigravityPool = "gemini"
	// AntigravityPoolThirdParty covers third-party models (Claude/GPT/OSS), which share the other pool.
	AntigravityPoolThirdParty AntigravityPool = "third_party"
)

// ClassifyAntigravityPool maps a model name to its Antigravity quota pool.
// Any model whose name contains "gemini" belongs to the Gemini pool; everything
// else (claude-*, gpt-oss-*, etc.) belongs to the third-party pool.
func ClassifyAntigravityPool(model string) AntigravityPool {
	if strings.Contains(strings.ToLower(strings.TrimSpace(model)), "gemini") {
		return AntigravityPoolGemini
	}
	return AntigravityPoolThirdParty
}

// antigravityPoolLabel returns a short human-readable label for a pool.
func antigravityPoolLabel(pool AntigravityPool) string {
	switch pool {
	case AntigravityPoolGemini:
		return "Gemini"
	case AntigravityPoolThirdParty:
		return "Claude/GPT"
	default:
		return string(pool)
	}
}
