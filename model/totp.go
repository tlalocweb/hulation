package model

import (
	"fmt"
	"strings"
	"time"

	"github.com/tlalocweb/hulation/utils"
	"gorm.io/gorm"
)

const sqlCreateAdminTotp = `
CREATE TABLE IF NOT EXISTS "admin_totp"
(
	"username" String,
	"totp_enabled" Bool,
	"totp_pending_setup" Bool,
	"totp_secret_encrypted" String,
	"totp_recovery_codes_hashed" Array(String),
	"totp_enabled_at" DateTime64(3),
	"updated_at" DateTime64(3)
)
ENGINE = ReplacingMergeTree(updated_at)
ORDER BY (username);`

type AdminTotpRecord struct {
	Username                string    `gorm:"column:username;primaryKey"`
	TotpEnabled             bool      `gorm:"column:totp_enabled"`
	TotpPendingSetup        bool      `gorm:"column:totp_pending_setup"`
	TotpSecretEncrypted     string    `gorm:"column:totp_secret_encrypted"`
	TotpRecoveryCodesHashed []string  `gorm:"column:totp_recovery_codes_hashed;type:Array(String)"`
	TotpEnabledAt           time.Time `gorm:"column:totp_enabled_at"`
	UpdatedAt               time.Time `gorm:"column:updated_at"`
}

func (AdminTotpRecord) TableName() string {
	return "admin_totp"
}

func AutoMigrateTotpModels(db *gorm.DB) error {
	return db.Exec(utils.SqlStr(sqlCreateAdminTotp)).Error
}

func GetAdminTotp(db *gorm.DB, username string) (*AdminTotpRecord, error) {
	var rec AdminTotpRecord
	err := db.Where("username = ?", username).First(&rec).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &rec, nil
}

func UpsertAdminTotp(db *gorm.DB, rec *AdminTotpRecord) error {
	rec.UpdatedAt = time.Now()
	return db.Create(rec).Error
}

// VerifyRecoveryCode checks a plaintext code against the hashed recovery codes.
// If matched, removes the used code and returns true.
func VerifyRecoveryCode(db *gorm.DB, rec *AdminTotpRecord, code string) (bool, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	for i, hashed := range rec.TotpRecoveryCodesHashed {
		match, err := utils.Argon2CompareHashAndSecret(code, hashed)
		if err != nil {
			continue
		}
		if match {
			// Remove the used code
			rec.TotpRecoveryCodesHashed = append(rec.TotpRecoveryCodesHashed[:i], rec.TotpRecoveryCodesHashed[i+1:]...)
			if err := UpsertAdminTotp(db, rec); err != nil {
				return true, fmt.Errorf("matched but failed to remove used code: %w", err)
			}
			return true, nil
		}
	}
	return false, nil
}
