package core

import (
	"fmt"
	"sort"
	"strings"
)

// BaselineStatus reports how much of the historical style baseline a run
// actually had. It is surfaced in the /generate JSON response and, when
// incomplete, as a bracketed note inside the generated document.
type BaselineStatus struct {
	Used    int      `json:"used"`
	Total   int      `json:"total"`
	Missing []string `json:"missing"`
}

// Complete reports whether every baseline document was available.
func (b BaselineStatus) Complete() bool {
	return b.Total > 0 && len(b.Missing) == 0
}

// DraftNote returns the internal note to inject into the document, or "" when
// the baseline is complete.
//
// This is a drafting artifact for the EP to clear before issuance -- NOT an
// ASTM data gap. A thin style baseline affects the prose voice; it says nothing
// about the availability of information required by E1527-21, and recording it
// as a data gap would put a false regulatory finding in a signed report.
func (b BaselineStatus) DraftNote() string {
	if b.Complete() {
		return ""
	}
	return fmt.Sprintf("[DRAFT NOTE — INTERNAL: generated against %d of %d historical baselines — remove before issuance]",
		b.Used, b.Total)
}

// LogValue keeps the status readable in structured logs.
func (b BaselineStatus) String() string {
	if b.Complete() {
		return fmt.Sprintf("%d/%d", b.Used, b.Total)
	}
	return fmt.Sprintf("%d/%d (missing: %s)", b.Used, b.Total, strings.Join(b.Missing, ", "))
}

// Status summarizes an already-loaded corpus.
func (c *HistoricalCorpus) Status() BaselineStatus {
	if c == nil {
		return BaselineStatus{}
	}
	missing := append([]string(nil), c.Failed...)
	sort.Strings(missing)
	return BaselineStatus{
		Used:    len(c.Docs),
		Total:   len(c.Docs) + len(c.Failed),
		Missing: missing,
	}
}
