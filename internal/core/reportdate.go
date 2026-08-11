package core

import (
	"log/slog"
	"time"

	// Embed the zone database. Scratch containers carry no system tzdata, so
	// without this LoadLocation fails and the date silently falls back to UTC
	// — which is the exact off-by-one this file exists to prevent.
	_ "time/tzdata"
)

// The running header's date is deterministic: it is the date the document was
// generated, not a fact the model may infer. The Providence Road draft went
// out stamped "July 29, 2026" from an August 5 run because ReportDate was left
// to the Template Compiler. See EP-caught error 7.

// houseTimeZone is the Matrix office zone. Cloud Run runs in UTC; an evening
// generation in Georgia is already tomorrow in UTC, which would stamp the
// wrong date on the header of every page.
const houseTimeZone = "America/New_York"

// reportDateLayout is the stamped house format: "August 11, 2026".
const reportDateLayout = "January 2, 2006"

// ReportDateNow returns today's date in the house format and time zone.
func ReportDateNow() string {
	return FormatReportDate(time.Now())
}

// FormatReportDate renders t in the house format, converted to the office time
// zone. Split from ReportDateNow so the conversion is testable against a fixed
// instant.
func FormatReportDate(t time.Time) string {
	loc, err := time.LoadLocation(houseTimeZone)
	if err != nil {
		// Not fatal: a report dated in the server's zone is still far better
		// than one the model invented. Logged because a wrong date on a signed
		// deliverable is worth noticing.
		slog.Warn("REPORT DATE ZONE UNAVAILABLE — falling back to server local time",
			"zone", houseTimeZone, "err", err)
		return t.Format(reportDateLayout)
	}
	return t.In(loc).Format(reportDateLayout)
}
