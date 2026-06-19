package migration

import (
	"fmt"

	"cpa-usage-keeper/internal/entities"

	"gorm.io/gorm"
)

// createAntigravityFiveHourEstimateStateMigration creates the table that caches the most
// recent projected full-cycle Antigravity 5h usage per (auth_index, pool), used as a fallback
// estimate while the current cycle is too fresh or fully consumed to extrapolate.
func createAntigravityFiveHourEstimateStateMigration(tx *gorm.DB) error {
	if err := tx.AutoMigrate(&entities.AntigravityFiveHourEstimateState{}); err != nil {
		return fmt.Errorf("auto migrate antigravity five hour estimate state: %w", err)
	}
	if err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uniq_antigravity_five_hour_estimate_auth_pool ON antigravity_five_hour_estimate_states (auth_index, pool)`).Error; err != nil {
		return fmt.Errorf("create antigravity five hour estimate state index: %w", err)
	}
	return nil
}
