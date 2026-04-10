package badactor

import (
	"time"

	"github.com/google/uuid"
	"github.com/tlalocweb/hulation/utils"
	"gorm.io/gorm"
)

// BadActorRecord is a single detection event stored in ClickHouse.
type BadActorRecord struct {
	ID            string    `gorm:"column:id;primaryKey"`
	CreatedAt     time.Time `gorm:"column:created_at"`
	IP            string    `gorm:"column:ip"`
	UserAgent     string    `gorm:"column:user_agent"`
	Method        string    `gorm:"column:method"`
	URL           string    `gorm:"column:url"`
	Host          string    `gorm:"column:host"`
	Reason        string    `gorm:"column:reason"`
	SignatureName string    `gorm:"column:signature_name"`
	Category      string    `gorm:"column:category"`
	Score         int       `gorm:"column:score"`
}

func (BadActorRecord) TableName() string { return "bad_actors" }

// AllowlistRecord is an admin-managed IP allowlist entry.
type AllowlistRecord struct {
	IP        string    `gorm:"column:ip;primaryKey" json:"ip"`
	CreatedAt time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at" json:"updated_at"`
	Reason    string    `gorm:"column:reason" json:"reason"`
	AddedBy   string    `gorm:"column:added_by" json:"added_by"`
}

func (AllowlistRecord) TableName() string { return "bad_actor_allowlist" }

const sqlCreateBadActors = `CREATE TABLE IF NOT EXISTS ` + "`bad_actors`" + `
(
    ` + "`id`" + ` String,
    ` + "`created_at`" + ` DateTime64(3),
    ` + "`ip`" + ` String,
    ` + "`user_agent`" + ` String,
    ` + "`method`" + ` String,
    ` + "`url`" + ` String,
    ` + "`host`" + ` String,
    ` + "`reason`" + ` String,
    ` + "`signature_name`" + ` String,
    ` + "`category`" + ` String,
    ` + "`score`" + ` Int32
)
ENGINE = MergeTree()
ORDER BY (ip, created_at);`

const sqlCreateAllowlist = `CREATE TABLE IF NOT EXISTS ` + "`bad_actor_allowlist`" + `
(
    ` + "`ip`" + ` String,
    ` + "`created_at`" + ` DateTime64(3),
    ` + "`updated_at`" + ` DateTime64(3),
    ` + "`reason`" + ` String,
    ` + "`added_by`" + ` String
)
ENGINE = ReplacingMergeTree(updated_at)
ORDER BY (ip);`

// AutoMigrateBadActorModels creates the bad actor tables in ClickHouse.
func AutoMigrateBadActorModels(db *gorm.DB) error {
	err := db.Exec(utils.SqlStr(sqlCreateBadActors)).Error
	if err != nil {
		baLog.Errorf("error creating bad_actors table: %s", err.Error())
		return err
	}
	err = db.Exec(utils.SqlStr(sqlCreateAllowlist)).Error
	if err != nil {
		baLog.Errorf("error creating bad_actor_allowlist table: %s", err.Error())
		return err
	}
	return nil
}

// InsertBadActorRecord writes a detection event to ClickHouse.
func InsertBadActorRecord(db *gorm.DB, ip, userAgent, method, url, host, reason, sigName, category string, score int) error {
	rec := BadActorRecord{
		ID:            uuid.Must(uuid.NewV7()).String(),
		CreatedAt:     time.Now(),
		IP:            ip,
		UserAgent:     userAgent,
		Method:        method,
		URL:           url,
		Host:          host,
		Reason:        reason,
		SignatureName: sigName,
		Category:      category,
		Score:         score,
	}
	return db.Create(&rec).Error
}

// LoadRecentBadActors loads IPs with their total scores from ClickHouse within the TTL window.
func LoadRecentBadActors(db *gorm.DB, ttl time.Duration) (map[string]int, error) {
	type result struct {
		IP    string `gorm:"column:ip"`
		Total int    `gorm:"column:total"`
	}
	var rows []result
	err := db.Raw("SELECT ip, sum(score) as total FROM bad_actors WHERE created_at > now() - interval ? second GROUP BY ip",
		int(ttl.Seconds())).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	m := make(map[string]int, len(rows))
	for _, r := range rows {
		m[r.IP] = r.Total
	}
	return m, nil
}

// LoadAllowlist loads all allowlisted IPs.
func LoadAllowlist(db *gorm.DB) ([]string, error) {
	var rows []AllowlistRecord
	err := db.Find(&rows).Error
	if err != nil {
		return nil, err
	}
	ips := make([]string, len(rows))
	for i, r := range rows {
		ips[i] = r.IP
	}
	return ips, nil
}

// AddToAllowlistDB adds an IP to the allowlist in ClickHouse.
func AddToAllowlistDB(db *gorm.DB, ip, reason, addedBy string) error {
	rec := AllowlistRecord{
		IP:        ip,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Reason:    reason,
		AddedBy:   addedBy,
	}
	return db.Create(&rec).Error
}

// RemoveFromAllowlistDB removes an IP from the allowlist.
func RemoveFromAllowlistDB(db *gorm.DB, ip string) error {
	return db.Where("ip = ?", ip).Delete(&AllowlistRecord{}).Error
}
