package quota

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository"
	"cpa-usage-keeper/internal/timeutil"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

const (
	antigravityExecutorType          = "AntigravityExecutor"
	antigravityWeeklyWindowDefault   = 7 * 24 * time.Hour
	antigravityFiveHourWindow        = 5 * time.Hour
	antigravityQuotaExhaustedKeyword = "QUOTA_EXHAUSTED"
)

// AntigravityWeeklyExhaustionEvent carries the information needed for weekly-cap detection.
type AntigravityWeeklyExhaustionEvent struct {
	AuthIndex      string
	Model          string
	Timestamp      time.Time
	FailStatusCode int
	FailBody       string
	ExecutorType   string
}

// IsAntigravityWeeklyExhaustion returns true if the event represents an Antigravity
// weekly quota exhaustion. A 429 with QUOTA_EXHAUSTED is necessary but not sufficient:
// the upstream body may carry a retryDelay or reset-time hint. When the parsed delay
// is ≤ 5 hours the exhaustion belongs to the 5h rolling window, not the weekly cap.
func IsAntigravityWeeklyExhaustion(e AntigravityWeeklyExhaustionEvent) bool {
	if !strings.EqualFold(strings.TrimSpace(e.ExecutorType), antigravityExecutorType) {
		return false
	}
	if e.FailStatusCode != 429 {
		return false
	}
	if !strings.Contains(strings.ToUpper(e.FailBody), antigravityQuotaExhaustedKeyword) {
		return false
	}
	if delay, ok := parseAntigravityRetryDelay(e.FailBody); ok && delay <= antigravityFiveHourWindow {
		return false
	}
	return true
}

var antigravityRetryAfterRegexp = regexp.MustCompile(`after\s+((?:\d+h)?(?:\d+m)?(?:\d+s)?)\.?`)

type antigravityErrorEnvelope struct {
	Error struct {
		Message string                   `json:"message"`
		Details []map[string]interface{} `json:"details"`
	} `json:"error"`
}

// parseAntigravityRetryDelay extracts a retry/reset delay from a Gemini-style 429 body.
// It checks, in order: RetryInfo.retryDelay, ErrorInfo.metadata.quotaResetDelay, and a
// human-readable "after Xh Ym Zs" pattern in the error message.
func parseAntigravityRetryDelay(body string) (time.Duration, bool) {
	if body == "" {
		return 0, false
	}
	var envelope antigravityErrorEnvelope
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		return 0, false
	}
	for _, detail := range envelope.Error.Details {
		if strVal(detail["@type"]) == "type.googleapis.com/google.rpc.RetryInfo" {
			if raw := strVal(detail["retryDelay"]); raw != "" {
				if d, err := time.ParseDuration(raw); err == nil {
					return d, true
				}
			}
		}
	}
	for _, detail := range envelope.Error.Details {
		if strVal(detail["@type"]) == "type.googleapis.com/google.rpc.ErrorInfo" {
			if metadata, ok := detail["metadata"].(map[string]interface{}); ok {
				if raw := strVal(metadata["quotaResetDelay"]); raw != "" {
					if d, err := time.ParseDuration(raw); err == nil {
						return d, true
					}
				}
			}
		}
	}
	if msg := envelope.Error.Message; msg != "" {
		if matches := antigravityRetryAfterRegexp.FindStringSubmatch(strings.ToLower(msg)); len(matches) > 1 && matches[1] != "" {
			if d, err := time.ParseDuration(matches[1]); err == nil {
				return d, true
			}
		}
	}
	return 0, false
}

func strVal(v interface{}) string {
	s, _ := v.(string)
	return s
}

// HandleAntigravityWeeklyExhaustion detects weekly quota exhaustion from an ingestion event
// and locks the accumulated USD as the estimated weekly limit. It is safe to call for every
// antigravity event — non-exhaustion events are ignored.
func HandleAntigravityWeeklyExhaustion(ctx context.Context, db *gorm.DB, e AntigravityWeeklyExhaustionEvent) error {
	if !IsAntigravityWeeklyExhaustion(e) {
		return nil
	}
	pool := ClassifyAntigravityPool(e.Model)
	now := timeutil.NormalizeStorageTime(e.Timestamp)
	if now.IsZero() {
		now = timeutil.NormalizeStorageTime(time.Now())
	}

	state, err := repository.GetAntigravityWeeklyQuotaState(ctx, db, e.AuthIndex, string(pool))
	if err != nil {
		return fmt.Errorf("load antigravity weekly quota state: %w", err)
	}

	// Repeated rejections inside an already-locked window are the upstream still saying
	// "exhausted"; they must not re-measure the cap or slide the reset forward (which would
	// keep the pool blocked indefinitely). Only refresh the last-exhausted timestamp.
	if state != nil && state.Confidence == string(entities.AntigravityWeeklyQuotaConfidenceLocked) && state.LastResetAt != nil {
		reset := timeutil.NormalizeStorageTime(*state.LastResetAt)
		if !reset.IsZero() && now.Before(reset) {
			state.LastExhaustedAt = &now
			return repository.UpsertAntigravityWeeklyQuotaState(ctx, db, *state)
		}
	}

	// New exhaustion: measure the spend accumulated over the current weekly window and lock it
	// as the estimated cap. WindowStart keeps the measurement window start so the display rows
	// can show the spend that hit the cap (~100%) until the projected reset.
	windowStart, _ := currentAntigravityWeeklyWindow(state, now)
	accumulatedUSD, err := accumulatePoolUSD(ctx, db, e.AuthIndex, pool, windowStart, &now)
	if err != nil {
		return fmt.Errorf("accumulate antigravity pool USD: %w", err)
	}

	nextReset := now.Add(antigravityWeeklyWindowDefault)

	logrus.WithFields(logrus.Fields{
		"auth_index":      e.AuthIndex,
		"pool":            string(pool),
		"accumulated_usd": accumulatedUSD,
		"window_start":    windowStart,
		"next_reset":      nextReset,
	}).Info("antigravity weekly quota exhaustion detected, locking estimate")

	return repository.UpsertAntigravityWeeklyQuotaState(ctx, db, entities.AntigravityWeeklyQuotaState{
		AuthIndex:         e.AuthIndex,
		Pool:              string(pool),
		WindowStart:       windowStart,
		EstimatedLimitUSD: &accumulatedUSD,
		LastExhaustedAt:   &now,
		LastResetAt:       &nextReset,
		Confidence:        string(entities.AntigravityWeeklyQuotaConfidenceLocked),
	})
}

// currentAntigravityWeeklyWindow returns the active [start, reset) weekly window for a state,
// rolling the persisted window forward by whole weeks until it contains now. A nil or zeroed
// state falls back to a trailing 7d window ending at now with an unknown (zero) reset.
func currentAntigravityWeeklyWindow(state *entities.AntigravityWeeklyQuotaState, now time.Time) (start time.Time, reset time.Time) {
	if state == nil || state.WindowStart.IsZero() {
		return now.Add(-antigravityWeeklyWindowDefault), time.Time{}
	}
	start = timeutil.NormalizeStorageTime(state.WindowStart)
	if state.LastResetAt != nil && !state.LastResetAt.IsZero() {
		reset = timeutil.NormalizeStorageTime(*state.LastResetAt)
	} else {
		reset = start.Add(antigravityWeeklyWindowDefault)
	}
	// Advance whole weeks until the window covers now; each elapsed window resets the pool.
	for !reset.IsZero() && !reset.After(now) {
		start = reset
		reset = reset.Add(antigravityWeeklyWindowDefault)
	}
	return start, reset
}

func accumulatePoolUSD(ctx context.Context, db *gorm.DB, authIndex string, pool AntigravityPool, start time.Time, end *time.Time) (float64, error) {
	geminiPool := pool == AntigravityPoolGemini
	stats, err := repository.SumUsageWindowStatsByAuthIndexAndPoolGemini(ctx, db, authIndex, start, end, geminiPool)
	if err != nil {
		return 0, err
	}
	return stats.Cost, nil
}
