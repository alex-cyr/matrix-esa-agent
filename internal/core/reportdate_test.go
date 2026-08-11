package core

import (
	"testing"
	"time"
)

func TestFormatReportDateUsesHouseFormat(t *testing.T) {
	got := FormatReportDate(time.Date(2026, time.August, 11, 15, 4, 5, 0, time.UTC))
	if want := "August 11, 2026"; got != want {
		t.Fatalf("FormatReportDate = %q, want %q", got, want)
	}
}

// Cloud Run is UTC. A report generated on a Georgia evening is already the
// next day in UTC, and the header date would be a day ahead on every page.
func TestFormatReportDateConvertsToOfficeZone(t *testing.T) {
	// 01:30 UTC on Aug 11 is 21:30 EDT on Aug 10.
	evening := time.Date(2026, time.August, 11, 1, 30, 0, 0, time.UTC)
	if got, want := FormatReportDate(evening), "August 10, 2026"; got != want {
		t.Fatalf("FormatReportDate(%s) = %q, want %q — a UTC evening must not roll the report date forward",
			evening.Format(time.RFC3339), got, want)
	}
}

// Standard time as well as daylight time, so the fix is not an accident of the
// summer offset.
func TestFormatReportDateHandlesStandardTime(t *testing.T) {
	winterEvening := time.Date(2026, time.January, 15, 2, 30, 0, 0, time.UTC) // 21:30 EST Jan 14
	if got, want := FormatReportDate(winterEvening), "January 14, 2026"; got != want {
		t.Fatalf("FormatReportDate(%s) = %q, want %q", winterEvening.Format(time.RFC3339), got, want)
	}
}

// The embedded tzdata import is load-bearing in a scratch container; if the
// zone ever fails to resolve, this catches it here rather than in a delivered
// report.
func TestHouseTimeZoneResolves(t *testing.T) {
	if _, err := time.LoadLocation(houseTimeZone); err != nil {
		t.Fatalf("house time zone %q did not resolve: %v (is _ \"time/tzdata\" still imported?)", houseTimeZone, err)
	}
}

func TestReportDateNowMatchesFormatter(t *testing.T) {
	if got, want := ReportDateNow(), FormatReportDate(time.Now()); got != want {
		t.Fatalf("ReportDateNow = %q, want %q", got, want)
	}
}
