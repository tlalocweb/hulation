package bolt

// ScheduledReports + ReportRuns persistence. Keyed by reportID /
// runID respectively. Values are JSON-marshalled structs.

import (
	"encoding/json"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

// StoredReport mirrors the ScheduledReport proto but decouples the
// store from the generated proto types.
type StoredReport struct {
	ID              string            `json:"id"`
	ServerID        string            `json:"server_id"`
	Name            string            `json:"name"`
	Cron            string            `json:"cron"`
	Timezone        string            `json:"timezone"`
	Recipients      []string          `json:"recipients"`
	TemplateVariant string            `json:"template_variant"` // "summary" | "detailed"
	// Filters are stored as a flat map<string,string> for cheap
	// round-tripping. The dispatcher + preview path translate back
	// into analyticsspec.Filters.
	Filters    map[string]string `json:"filters,omitempty"`
	Enabled    bool              `json:"enabled"`
	CreatedAt  time.Time         `json:"created_at"`
	UpdatedAt  time.Time         `json:"updated_at"`
	NextFireAt time.Time         `json:"next_fire_at,omitempty"`
}

// StoredReportRun captures one dispatch attempt.
type StoredReportRun struct {
	ID         string    `json:"id"`
	ReportID   string    `json:"report_id"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	Status     string    `json:"status"` // "success" | "failed" | "retrying"
	Attempt    int32     `json:"attempt"`
	Error      string    `json:"error,omitempty"`
	Recipients []string  `json:"recipients,omitempty"`
}

func PutReport(r StoredReport) (StoredReport, error) {
	if r.ID == "" || r.ServerID == "" {
		return r, fmt.Errorf("report: id and server_id required")
	}
	db := Get()
	if db == nil {
		return r, ErrNotOpen
	}
	now := time.Now().UTC()
	err := db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(BucketReports))
		if existing := b.Get([]byte(r.ID)); existing != nil {
			var prev StoredReport
			if uerr := json.Unmarshal(existing, &prev); uerr == nil && !prev.CreatedAt.IsZero() {
				r.CreatedAt = prev.CreatedAt
			}
		}
		if r.CreatedAt.IsZero() {
			r.CreatedAt = now
		}
		r.UpdatedAt = now
		data, merr := json.Marshal(&r)
		if merr != nil {
			return merr
		}
		return b.Put([]byte(r.ID), data)
	})
	return r, err
}

func GetReport(reportID string) (*StoredReport, error) {
	db := Get()
	if db == nil {
		return nil, ErrNotOpen
	}
	var out *StoredReport
	err := db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket([]byte(BucketReports)).Get([]byte(reportID))
		if v == nil {
			return nil
		}
		var r StoredReport
		if uerr := json.Unmarshal(v, &r); uerr != nil {
			return uerr
		}
		out = &r
		return nil
	})
	return out, err
}

func DeleteReport(reportID string) error {
	db := Get()
	if db == nil {
		return ErrNotOpen
	}
	return db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(BucketReports)).Delete([]byte(reportID))
	})
}

func ListReports(serverID string) ([]StoredReport, error) {
	db := Get()
	if db == nil {
		return nil, ErrNotOpen
	}
	var out []StoredReport
	err := db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(BucketReports)).ForEach(func(_, v []byte) error {
			var r StoredReport
			if uerr := json.Unmarshal(v, &r); uerr != nil {
				return nil
			}
			if serverID != "" && r.ServerID != serverID {
				return nil
			}
			out = append(out, r)
			return nil
		})
	})
	return out, err
}

// AppendReportRun stores a dispatch-attempt row. Rows are append-
// only; the dispatcher never updates an existing run (retries write
// a new row with incremented attempt).
func AppendReportRun(run StoredReportRun) error {
	if run.ID == "" || run.ReportID == "" {
		return fmt.Errorf("run: id and report_id required")
	}
	db := Get()
	if db == nil {
		return ErrNotOpen
	}
	return db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(BucketReportRuns))
		data, err := json.Marshal(&run)
		if err != nil {
			return err
		}
		return b.Put([]byte(run.ID), data)
	})
}

// ListReportRuns returns runs for a given report_id, most recent
// first. limit=0 returns all rows (expect small — each report
// typically has tens of runs at most).
func ListReportRuns(reportID string, limit int) ([]StoredReportRun, error) {
	db := Get()
	if db == nil {
		return nil, ErrNotOpen
	}
	var out []StoredReportRun
	err := db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(BucketReportRuns)).ForEach(func(_, v []byte) error {
			var r StoredReportRun
			if uerr := json.Unmarshal(v, &r); uerr != nil {
				return nil
			}
			if reportID != "" && r.ReportID != reportID {
				return nil
			}
			out = append(out, r)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	// Sort descending by StartedAt. Small N, bubble-sort is fine.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[i].StartedAt.Before(out[j].StartedAt) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
