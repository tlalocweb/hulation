// Package render produces email-safe HTML for a ScheduledReport.
//
// Phase 3.4 ships a minimal summary template: a header, four KPI
// rows, and a plaintext filter summary. No inline SVG yet — the
// Phase-3.5 dispatcher will grow a small SVG-sparkline generator
// so the email has visual charts without relying on remote fetches
// (most email clients block).
//
// Tight dependency footprint: stdlib `html/template` + `time`. Every
// field rendered is escaped via the template engine.

package render

import (
	"bytes"
	"fmt"
	"html/template"
	"time"
)

// SummaryInput is the data the summary template reads. Deliberately
// small; the renderer is responsible for any human-readable
// formatting (numbers, durations, percentages).
type SummaryInput struct {
	ReportName    string
	ServerID      string
	From          time.Time
	To            time.Time
	TimezoneLabel string

	Visitors                  int64
	Pageviews                 int64
	BounceRate                float64 // 0..1
	AvgSessionDurationSeconds float64

	// Optional top-N rows from /api/v1/analytics/pages. Rendered as a
	// bullet list in the detailed variant, skipped in summary.
	TopPages []TopRow
}

// TopRow is one entry in a bullet list.
type TopRow struct {
	Key       string
	Visitors  int64
	Pageviews int64
}

// Variant discriminates between the summary and detailed templates.
type Variant int

const (
	VariantSummary Variant = iota
	VariantDetailed
)

// Render emits the HTML body for an email. Also returns a subject
// line.
func Render(variant Variant, in SummaryInput) (html string, subject string, err error) {
	subject = fmt.Sprintf("%s — %s to %s",
		in.ReportName,
		in.From.Format("2006-01-02"),
		in.To.Format("2006-01-02"),
	)
	tmplSrc := summaryTemplate
	if variant == VariantDetailed {
		tmplSrc = detailedTemplate
	}
	t, err := template.New("report").Funcs(funcs()).Parse(tmplSrc)
	if err != nil {
		return "", subject, fmt.Errorf("parse template: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, in); err != nil {
		return "", subject, fmt.Errorf("execute template: %w", err)
	}
	return buf.String(), subject, nil
}

func funcs() template.FuncMap {
	return template.FuncMap{
		"fmtInt": func(n int64) string {
			// Thousands separator by hand — no strconv/locale dep.
			s := fmt.Sprintf("%d", n)
			if len(s) <= 3 {
				return s
			}
			var out []byte
			pre := len(s) % 3
			if pre > 0 {
				out = append(out, s[:pre]...)
				if len(s) > pre {
					out = append(out, ',')
				}
			}
			for i := pre; i < len(s); i += 3 {
				out = append(out, s[i:i+3]...)
				if i+3 < len(s) {
					out = append(out, ',')
				}
			}
			return string(out)
		},
		"fmtPct": func(r float64) string { return fmt.Sprintf("%.1f%%", r*100) },
		"fmtDuration": func(s float64) string {
			sec := int64(s + 0.5)
			if sec < 60 {
				return fmt.Sprintf("%ds", sec)
			}
			m := sec / 60
			return fmt.Sprintf("%dm %ds", m, sec%60)
		},
		"fmtDate": func(t time.Time) string { return t.Format("2006-01-02") },
	}
}

// Both templates are deliberately email-client safe: tables for
// layout, inline styles, no <script>, no remote assets.

const summaryTemplate = `<!DOCTYPE html>
<html>
<head>
  <meta charset="utf-8">
  <title>{{.ReportName}}</title>
</head>
<body style="margin:0;padding:0;background:#f8fafc;font-family:system-ui,-apple-system,sans-serif;">
  <table role="presentation" cellspacing="0" cellpadding="0" width="100%" style="max-width:640px;margin:0 auto;background:#fff;">
    <tr>
      <td style="padding:24px 24px 8px;">
        <h1 style="margin:0 0 4px;font-size:20px;color:#0f172a;">{{.ReportName}}</h1>
        <p style="margin:0;font-size:13px;color:#64748b;">
          {{.ServerID}} · {{fmtDate .From}} – {{fmtDate .To}}{{if .TimezoneLabel}} ({{.TimezoneLabel}}){{end}}
        </p>
      </td>
    </tr>
    <tr>
      <td style="padding:8px 24px 16px;">
        <table role="presentation" cellspacing="0" cellpadding="0" width="100%">
          <tr>
            <td style="padding:12px;background:#f1f5f9;border-radius:6px;width:50%;vertical-align:top;">
              <div style="font-size:11px;color:#64748b;text-transform:uppercase;letter-spacing:0.05em;">Visitors</div>
              <div style="font-size:26px;font-weight:600;color:#0f172a;margin-top:2px;">{{fmtInt .Visitors}}</div>
            </td>
            <td style="padding:12px;background:#f1f5f9;border-radius:6px;width:50%;vertical-align:top;">
              <div style="font-size:11px;color:#64748b;text-transform:uppercase;letter-spacing:0.05em;">Pageviews</div>
              <div style="font-size:26px;font-weight:600;color:#0f172a;margin-top:2px;">{{fmtInt .Pageviews}}</div>
            </td>
          </tr>
          <tr><td colspan="2" style="height:8px;"></td></tr>
          <tr>
            <td style="padding:12px;background:#f1f5f9;border-radius:6px;vertical-align:top;">
              <div style="font-size:11px;color:#64748b;text-transform:uppercase;letter-spacing:0.05em;">Bounce rate</div>
              <div style="font-size:22px;font-weight:600;color:#0f172a;margin-top:2px;">{{fmtPct .BounceRate}}</div>
            </td>
            <td style="padding:12px;background:#f1f5f9;border-radius:6px;vertical-align:top;">
              <div style="font-size:11px;color:#64748b;text-transform:uppercase;letter-spacing:0.05em;">Avg session</div>
              <div style="font-size:22px;font-weight:600;color:#0f172a;margin-top:2px;">{{fmtDuration .AvgSessionDurationSeconds}}</div>
            </td>
          </tr>
        </table>
      </td>
    </tr>
    <tr>
      <td style="padding:8px 24px 24px;font-size:12px;color:#64748b;">
        Sent by Hula Analytics. Log in to your dashboard to explore the underlying data.
      </td>
    </tr>
  </table>
</body>
</html>`

const detailedTemplate = `<!DOCTYPE html>
<html>
<head>
  <meta charset="utf-8">
  <title>{{.ReportName}}</title>
</head>
<body style="margin:0;padding:0;background:#f8fafc;font-family:system-ui,-apple-system,sans-serif;">
  <table role="presentation" cellspacing="0" cellpadding="0" width="100%" style="max-width:640px;margin:0 auto;background:#fff;">
    <tr>
      <td style="padding:24px 24px 8px;">
        <h1 style="margin:0 0 4px;font-size:20px;color:#0f172a;">{{.ReportName}}</h1>
        <p style="margin:0;font-size:13px;color:#64748b;">
          {{.ServerID}} · {{fmtDate .From}} – {{fmtDate .To}}{{if .TimezoneLabel}} ({{.TimezoneLabel}}){{end}}
        </p>
      </td>
    </tr>
    <tr>
      <td style="padding:8px 24px 16px;">
        <table role="presentation" cellspacing="0" cellpadding="0" width="100%">
          <tr>
            <td style="padding:12px;background:#f1f5f9;border-radius:6px;width:50%;vertical-align:top;">
              <div style="font-size:11px;color:#64748b;text-transform:uppercase;letter-spacing:0.05em;">Visitors</div>
              <div style="font-size:26px;font-weight:600;color:#0f172a;margin-top:2px;">{{fmtInt .Visitors}}</div>
            </td>
            <td style="padding:12px;background:#f1f5f9;border-radius:6px;width:50%;vertical-align:top;">
              <div style="font-size:11px;color:#64748b;text-transform:uppercase;letter-spacing:0.05em;">Pageviews</div>
              <div style="font-size:26px;font-weight:600;color:#0f172a;margin-top:2px;">{{fmtInt .Pageviews}}</div>
            </td>
          </tr>
          <tr><td colspan="2" style="height:8px;"></td></tr>
          <tr>
            <td style="padding:12px;background:#f1f5f9;border-radius:6px;vertical-align:top;">
              <div style="font-size:11px;color:#64748b;text-transform:uppercase;letter-spacing:0.05em;">Bounce rate</div>
              <div style="font-size:22px;font-weight:600;color:#0f172a;margin-top:2px;">{{fmtPct .BounceRate}}</div>
            </td>
            <td style="padding:12px;background:#f1f5f9;border-radius:6px;vertical-align:top;">
              <div style="font-size:11px;color:#64748b;text-transform:uppercase;letter-spacing:0.05em;">Avg session</div>
              <div style="font-size:22px;font-weight:600;color:#0f172a;margin-top:2px;">{{fmtDuration .AvgSessionDurationSeconds}}</div>
            </td>
          </tr>
        </table>
      </td>
    </tr>
    {{if .TopPages}}
    <tr>
      <td style="padding:8px 24px 16px;">
        <h2 style="margin:0 0 8px;font-size:14px;color:#0f172a;">Top pages</h2>
        <table role="presentation" cellspacing="0" cellpadding="0" width="100%" style="font-size:13px;">
          {{range .TopPages}}
          <tr>
            <td style="padding:6px 0;color:#0f172a;">{{.Key}}</td>
            <td style="padding:6px 0;color:#64748b;text-align:right;">{{fmtInt .Visitors}} · {{fmtInt .Pageviews}}</td>
          </tr>
          {{end}}
        </table>
      </td>
    </tr>
    {{end}}
    <tr>
      <td style="padding:8px 24px 24px;font-size:12px;color:#64748b;">
        Sent by Hula Analytics. Log in to your dashboard to explore the underlying data.
      </td>
    </tr>
  </table>
</body>
</html>`
