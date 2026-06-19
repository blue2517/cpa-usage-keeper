package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/timeutil"

	"gorm.io/gorm"
)

// ListAntigravityFiveHourEstimateStatesByAuthIndex returns all pool estimate states for one auth.
func ListAntigravityFiveHourEstimateStatesByAuthIndex(ctx context.Context, db *gorm.DB, authIndex string) ([]entities.AntigravityFiveHourEstimateState, error) {
	if db == nil {
		return nil, fmt.Errorf("database is nil")
	}
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" {
		return nil, fmt.Errorf("auth_index is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var states []entities.AntigravityFiveHourEstimateState
	if err := db.WithContext(ctx).Where("auth_index = ?", authIndex).Find(&states).Error; err != nil {
		return nil, fmt.Errorf("list antigravity five hour estimate states: %w", err)
	}
	return states, nil
}

// UpsertAntigravityFiveHourEstimateState inserts or updates the cached 5h cycle estimate
// for one (auth_index, pool) pair. CreatedAt is preserved on update.
func UpsertAntigravityFiveHourEstimateState(ctx context.Context, db *gorm.DB, state entities.AntigravityFiveHourEstimateState) error {
	if db == nil {
		return fmt.Errorf("database is nil")
	}
	state.AuthIndex = strings.TrimSpace(state.AuthIndex)
	state.Pool = strings.TrimSpace(state.Pool)
	if state.AuthIndex == "" || state.Pool == "" {
		return fmt.Errorf("auth_index and pool are required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := timeutil.NormalizeStorageTime(time.Now())
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing entities.AntigravityFiveHourEstimateState
		err := tx.Where("auth_index = ? AND pool = ?", state.AuthIndex, state.Pool).First(&existing).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("load antigravity five hour estimate state for upsert: %w", err)
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			state.CreatedAt = now
			state.UpdatedAt = now
			if err := tx.Create(&state).Error; err != nil {
				return fmt.Errorf("create antigravity five hour estimate state: %w", err)
			}
			return nil
		}
		state.ID = existing.ID
		state.CreatedAt = existing.CreatedAt
		state.UpdatedAt = now
		if err := tx.Model(&entities.AntigravityFiveHourEstimateState{}).
			Where("id = ?", existing.ID).
			Select("estimated_tokens", "estimated_cost_usd", "cycle_reset_at", "updated_at").
			Updates(map[string]any{
				"estimated_tokens":   state.EstimatedTokens,
				"estimated_cost_usd": state.EstimatedCostUSD,
				"cycle_reset_at":     state.CycleResetAt,
				"updated_at":         now,
			}).Error; err != nil {
			return fmt.Errorf("update antigravity five hour estimate state: %w", err)
		}
		return nil
	})
}
