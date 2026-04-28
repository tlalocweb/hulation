package render

import (
	"strings"
	"testing"
	"time"
)

func TestRenderSummary(t *testing.T) {
	in := SummaryInput{
		ReportName:                "Weekly overview",
		ServerID:                  "testsite",
		From:                      time.Date(2026, 4, 17, 0, 0, 0, 0, time.UTC),
		To:                        time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC),
		TimezoneLabel:             "UTC",
		Visitors:                  12_345,
		Pageviews:                 98_765,
		BounceRate:                0.423,
		AvgSessionDurationSeconds: 185.5,
	}
	html, subject, err := Render(VariantSummary, in)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(subject, "Weekly overview") || !strings.Contains(subject, "2026-04-17") {
		t.Fatalf("subject missing expected fields: %s", subject)
	}
	for _, want := range []string{
		"Weekly overview",
		"testsite",
		"12,345",  // thousands separator
		"98,765",
		"42.3%",   // bounce rate pct
		"3m 6s",   // 185.5s → 186s → 3m 6s
	} {
		if !strings.Contains(html, want) {
			t.Errorf("html missing %q", want)
		}
	}
	if strings.Contains(html, "<script") {
		t.Errorf("template leaked a <script> tag")
	}
}

func TestRenderDetailedWithTopPages(t *testing.T) {
	in := SummaryInput{
		ReportName: "Daily",
		ServerID:   "testsite",
		From:       time.Date(2026, 4, 23, 0, 0, 0, 0, time.UTC),
		To:         time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC),
		Visitors:   10, Pageviews: 20,
		TopPages: []TopRow{
			{Key: "/", Visitors: 5, Pageviews: 9},
			{Key: "/about", Visitors: 3, Pageviews: 4},
		},
	}
	html, _, err := Render(VariantDetailed, in)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{"/about", "Top pages"} {
		if !strings.Contains(html, want) {
			t.Errorf("detailed html missing %q", want)
		}
	}
}
