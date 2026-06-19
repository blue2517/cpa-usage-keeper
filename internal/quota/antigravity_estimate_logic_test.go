package quota

import (
	"testing"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/timeutil"
)

func TestCurrentAntigravityWeeklyWindow(t *testing.T) {
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	week := antigravityWeeklyWindowDefault

	t.Run("nil state falls back to trailing 7d with unknown reset", func(t *testing.T) {
		now := timeutil.NormalizeStorageTime(base)
		start, reset := currentAntigravityWeeklyWindow(nil, now)
		if !start.Equal(now.Add(-week)) {
			t.Fatalf("expected start %v, got %v", now.Add(-week), start)
		}
		if !reset.IsZero() {
			t.Fatalf("expected zero reset, got %v", reset)
		}
	})

	t.Run("active window is returned unchanged", func(t *testing.T) {
		windowStart := timeutil.NormalizeStorageTime(base)
		reset := windowStart.Add(week)
		state := &entities.AntigravityWeeklyQuotaState{WindowStart: windowStart, LastResetAt: &reset}
		now := windowStart.Add(2 * 24 * time.Hour)
		gotStart, gotReset := currentAntigravityWeeklyWindow(state, now)
		if !gotStart.Equal(windowStart) {
			t.Fatalf("expected start %v, got %v", windowStart, gotStart)
		}
		if !gotReset.Equal(reset) {
			t.Fatalf("expected reset %v, got %v", reset, gotReset)
		}
	})

	t.Run("elapsed window rolls forward by whole weeks", func(t *testing.T) {
		windowStart := timeutil.NormalizeStorageTime(base)
		reset := windowStart.Add(week)
		state := &entities.AntigravityWeeklyQuotaState{WindowStart: windowStart, LastResetAt: &reset}
		// now is ~2.5 weeks past the original window start: window should roll to the 3rd.
		now := windowStart.Add(2*week + 3*24*time.Hour)
		gotStart, gotReset := currentAntigravityWeeklyWindow(state, now)
		wantStart := windowStart.Add(2 * week)
		wantReset := windowStart.Add(3 * week)
		if !gotStart.Equal(wantStart) {
			t.Fatalf("expected rolled start %v, got %v", wantStart, gotStart)
		}
		if !gotReset.Equal(wantReset) {
			t.Fatalf("expected rolled reset %v, got %v", wantReset, gotReset)
		}
		if !now.Before(gotReset) || now.Before(gotStart) {
			t.Fatalf("expected now %v within [%v, %v)", now, gotStart, gotReset)
		}
	})
}

func TestProjectAntigravityCycleUsage(t *testing.T) {
	tokens := func(v int64) *int64 { return &v }
	cost := func(v float64) *float64 { return &v }

	cases := []struct {
		name       string
		row        QuotaRow
		wantOK     bool
		wantTokens int64
		wantCost   float64
	}{
		{
			name:       "partial cycle extrapolates to full cycle",
			row:        QuotaRow{UsedPercent: cost(25), WindowUsageTokens: tokens(1_000_000), WindowUsageCost: cost(2.5)},
			wantOK:     true,
			wantTokens: 4_000_000,
			wantCost:   10,
		},
		{
			name:   "fresh cycle (0%) cannot extrapolate",
			row:    QuotaRow{UsedPercent: cost(0), WindowUsageTokens: tokens(0), WindowUsageCost: cost(0)},
			wantOK: false,
		},
		{
			name:   "maxed cycle (100%) cannot extrapolate",
			row:    QuotaRow{UsedPercent: cost(100), WindowUsageTokens: tokens(5_000), WindowUsageCost: cost(9)},
			wantOK: false,
		},
		{
			name:   "no spend cannot extrapolate",
			row:    QuotaRow{UsedPercent: cost(50), WindowUsageTokens: tokens(0), WindowUsageCost: cost(0)},
			wantOK: false,
		},
		{
			name:   "missing used percent cannot extrapolate",
			row:    QuotaRow{WindowUsageTokens: tokens(1_000), WindowUsageCost: cost(1)},
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotTokens, gotCost, ok := projectAntigravityCycleUsage(tc.row)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if gotTokens != tc.wantTokens {
				t.Fatalf("tokens = %d, want %d", gotTokens, tc.wantTokens)
			}
			if gotCost != tc.wantCost {
				t.Fatalf("cost = %v, want %v", gotCost, tc.wantCost)
			}
		})
	}
}

func TestAntigravityPoolUsageWindowAlignsToReset(t *testing.T) {
	windowSeconds := int64(5 * 60 * 60)
	now := timeutil.NormalizeStorageTime(time.Date(2026, 6, 2, 3, 0, 0, 0, time.UTC))

	t.Run("aligns to in-progress reset", func(t *testing.T) {
		resetAt := time.Date(2026, 6, 2, 5, 0, 0, 0, time.UTC)
		row := QuotaRow{Window: &QuotaWindow{Seconds: &windowSeconds}, ResetAt: timeutil.FormatStorageTime(resetAt)}
		start, end, ok := antigravityPoolUsageWindow(row, now)
		if !ok {
			t.Fatal("expected ok")
		}
		wantStart := timeutil.NormalizeStorageTime(resetAt).Add(-time.Duration(windowSeconds) * time.Second)
		if !start.Equal(wantStart) {
			t.Fatalf("expected aligned start %v, got %v", wantStart, start)
		}
		if !end.Equal(now) {
			t.Fatalf("expected end %v, got %v", now, end)
		}
	})

	t.Run("falls back to fixed lookback when reset is missing", func(t *testing.T) {
		row := QuotaRow{Window: &QuotaWindow{Seconds: &windowSeconds}}
		start, _, ok := antigravityPoolUsageWindow(row, now)
		if !ok {
			t.Fatal("expected ok")
		}
		wantStart := now.Add(-time.Duration(windowSeconds) * time.Second)
		if !start.Equal(wantStart) {
			t.Fatalf("expected fixed start %v, got %v", wantStart, start)
		}
	})

	t.Run("falls back when reset already elapsed (fresh cycle)", func(t *testing.T) {
		resetAt := time.Date(2026, 6, 2, 2, 0, 0, 0, time.UTC) // before now
		row := QuotaRow{Window: &QuotaWindow{Seconds: &windowSeconds}, ResetAt: timeutil.FormatStorageTime(resetAt)}
		start, _, ok := antigravityPoolUsageWindow(row, now)
		if !ok {
			t.Fatal("expected ok")
		}
		wantStart := now.Add(-time.Duration(windowSeconds) * time.Second)
		if !start.Equal(wantStart) {
			t.Fatalf("expected fixed start %v, got %v", wantStart, start)
		}
	})
}
