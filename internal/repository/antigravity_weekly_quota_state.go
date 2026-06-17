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

// GetAntigravityWeeklyQuotaState reads the persisted weekly quota state for one
// (auth_index, pool) pair. A missing row returns (nil, nil).
func GetAntigravityWeeklyQuotaState(ctx context.Context, db *gorm.DB, authIndex string, pool string) (*entities.AntigravityWeeklyQuotaState, error) {
	if db == nil {
		return nil, fmt.Errorf("database is nil")
	}
	authIndex = strings.TrimSpace(authIndex)
	pool = strings.TrimSpace(pool)
	if authIndex == "" || pool == "" {
		return nil, fmt.Errorf("auth_index and pool are required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var state entities.AntigravityWeeklyQuotaState
	err := db.WithContext(ctx).Where("auth_index = ? AND pool = ?", authIndex, pool).First(&state).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("get antigravity weekly quota state: %w", err)
	}
	return &state, nil
}

// ListAntigravityWeeklyQuotaStatesByAuthIndex returns all pool states for one auth.
func ListAntigravityWeeklyQuotaStatesByAuthIndex(ctx context.Context, db *gorm.DB, authIndex string) ([]entities.AntigravityWeeklyQuotaState, error) {
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
	var states []entities.AntigravityWeeklyQuotaState
	if err := db.WithContext(ctx).Where("auth_index = ?", authIndex).Find(&states).Error; err != nil {
		return nil, fmt.Errorf("list antigravity weekly quota states: %w", err)
	}
	return states, nil
}

// UpsertAntigravityWeeklyQuotaState inserts or updates the weekly quota state for one
// (auth_index, pool) pair. CreatedAt is preserved on update.
func UpsertAntigravityWeeklyQuotaState(ctx context.Context, db *gorm.DB, state entities.AntigravityWeeklyQuotaState) error {
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
		var existing entities.AntigravityWeeklyQuotaState
		err := tx.Where("auth_index = ? AND pool = ?", state.AuthIndex, state.Pool).First(&existing).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("load antigravity weekly quota state for upsert: %w", err)
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			state.CreatedAt = now
			state.UpdatedAt = now
			if err := tx.Create(&state).Error; err != nil {
				return fmt.Errorf("create antigravity weekly quota state: %w", err)
			}
			return nil
		}
		state.ID = existing.ID
		state.CreatedAt = existing.CreatedAt
		state.UpdatedAt = now
		if err := tx.Model(&entities.AntigravityWeeklyQuotaState{}).
			Where("id = ?", existing.ID).
			Select("window_start", "estimated_limit_usd", "last_exhausted_at", "last_reset_at", "confidence", "updated_at").
			Updates(map[string]any{
				"window_start":        timeutil.NormalizeStorageTime(state.WindowStart),
				"estimated_limit_usd": state.EstimatedLimitUSD,
				"last_exhausted_at":   state.LastExhaustedAt,
				"last_reset_at":       state.LastResetAt,
				"confidence":          state.Confidence,
				"updated_at":          now,
			}).Error; err != nil {
			return fmt.Errorf("update antigravity weekly quota state: %w", err)
		}
		return nil
	})
}
