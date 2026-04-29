package bolt

// ScheduledReports + ReportRuns persistence. Keyed by reportID /
// runID respectively. Values are JSON-marshalled structs.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/tlalocweb/hulation/pkg/store/storage"
)

// StoredReport mirrors the ScheduledReport proto but decouples the
// store from the generated proto types.
type StoredReport struct {
	ID              string `json:"id"`
	ServerID        string `json:"server_id"`
	Name            string `json:"name"`
	Cron            string `json:"cron"`
	Timezone        string `json:"timezone"`
	Recipients      []string `json:"recipients"`
	TemplateVariant string `json:"template_variant"` // "summary" | "detailed"
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

func reportKey(id string) string    { return "reports/" + id }
func reportRunKey(id string) string { return "report_runs/" + id }

func PutReport(ctx context.Context, s storage.Storage, r StoredReport) (StoredReport, error) {
	if s == nil {
		return r, ErrNotOpen
	}
	if r.ID == "" || r.ServerID == "" {
		return r, fmt.Errorf("report: id and server_id required")
	}
	now := time.Now().UTC()
	err := s.Mutate(ctx, reportKey(r.ID), func(current []byte) ([]byte, error) {
		if len(current) > 0 {
			var prev StoredReport
			if uerr := json.Unmarshal(current, &prev); uerr == nil && !prev.CreatedAt.IsZero() {
				r.CreatedAt = prev.CreatedAt
			}
		}
		if r.CreatedAt.IsZero() {
			r.CreatedAt = now
		}
		r.UpdatedAt = now
		return json.Marshal(&r)
	})
	return r, err
}

func GetReport(ctx context.Context, s storage.Storage, reportID string) (*StoredReport, error) {
	if s == nil {
		return nil, ErrNotOpen
	}
	v, err := s.Get(ctx, reportKey(reportID))
	if errors.Is(err, storage.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r StoredReport
	if uerr := json.Unmarshal(v, &r); uerr != nil {
		return nil, uerr
	}
	return &r, nil
}

func DeleteReport(ctx context.Context, s storage.Storage, reportID string) error {
	if s == nil {
		return ErrNotOpen
	}
	return s.Delete(ctx, reportKey(reportID))
}

func ListReports(ctx context.Context, s storage.Storage, serverID string) ([]StoredReport, error) {
	if s == nil {
		return nil, ErrNotOpen
	}
	rows, err := s.List(ctx, "reports/")
	if err != nil {
		return nil, err
	}
	out := make([]StoredReport, 0, len(rows))
	for _, v := range rows {
		var r StoredReport
		if uerr := json.Unmarshal(v, &r); uerr != nil {
			continue
		}
		if serverID != "" && r.ServerID != serverID {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// AppendReportRun stores a dispatch-attempt row. Rows are append-
// only; the dispatcher never updates an existing run (retries write
// a new row with incremented attempt).
func AppendReportRun(ctx context.Context, s storage.Storage, run StoredReportRun) error {
	if s == nil {
		return ErrNotOpen
	}
	if run.ID == "" || run.ReportID == "" {
		return fmt.Errorf("run: id and report_id required")
	}
	data, err := json.Marshal(&run)
	if err != nil {
		return err
	}
	return s.Put(ctx, reportRunKey(run.ID), data)
}

// ListReportRuns returns runs for a given report_id, most recent
// first. limit=0 returns all rows (expect small — each report
// typically has tens of runs at most).
func ListReportRuns(ctx context.Context, s storage.Storage, reportID string, limit int) ([]StoredReportRun, error) {
	if s == nil {
		return nil, ErrNotOpen
	}
	rows, err := s.List(ctx, "report_runs/")
	if err != nil {
		return nil, err
	}
	out := make([]StoredReportRun, 0, len(rows))
	for _, v := range rows {
		var r StoredReportRun
		if uerr := json.Unmarshal(v, &r); uerr != nil {
			continue
		}
		if reportID != "" && r.ReportID != reportID {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
