package migration

import (
	"fmt"

	"cpa-usage-keeper/internal/entities"

	"gorm.io/gorm"
)

// createAntigravityWeeklyQuotaStateMigration creates the table that persists the
// heuristically estimated Antigravity weekly quota per (auth_index, pool).
func createAntigravityWeeklyQuotaStateMigration(tx *gorm.DB) error {
	if err := tx.AutoMigrate(&entities.AntigravityWeeklyQuotaState{}); err != nil {
		return fmt.Errorf("auto migrate antigravity weekly quota state: %w", err)
	}
	if err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uniq_antigravity_weekly_quota_state_auth_pool ON antigravity_weekly_quota_states (auth_index, pool)`).Error; err != nil {
		return fmt.Errorf("create antigravity weekly quota state index: %w", err)
	}
	return nil
}
