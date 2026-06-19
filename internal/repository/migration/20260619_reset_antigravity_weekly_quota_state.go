package migration

import "gorm.io/gorm"

// resetAntigravityWeeklyQuotaStateMigration clears all locked weekly quota states because
// the prior detection logic could not distinguish 5h exhaustion from weekly exhaustion.
// After this migration the weekly detector requires an explicit retry delay > 5h to lock.
func resetAntigravityWeeklyQuotaStateMigration(tx *gorm.DB) error {
	return tx.Exec("DELETE FROM antigravity_weekly_quota_states").Error
}
