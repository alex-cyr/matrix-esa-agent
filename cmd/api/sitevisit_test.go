package main

import (
	"strings"
	"testing"

	"github.com/matrix-engineering/matrix-esa-agent/internal/core"
)

func detected(cats ...string) []core.CategorizedFile {
	var out []core.CategorizedFile
	for i, c := range cats {
		out = append(out, core.CategorizedFile{OriginalName: string(rune('a'+i)) + ".pdf", Category: c})
	}
	return out
}

// The core of the phase's design principle, stated as a test: when the source
// document IS present, the questions must not be asked. Asking anyway launders
// an extraction failure into a green run -- the EP fills the field, the report
// looks complete, and the parser bug survives to the next project.
func TestChecklistPresentSuppressesEverySiteVisitQuestion(t *testing.T) {
	got := siteVisitQuestions(detected("EDR Historical Package", siteReconChecklistCategory, "Filio Site Photos"))
	if len(got) != 0 {
		var ids []string
		for _, q := range got {
			ids = append(ids, q.ID)
		}
		t.Errorf("a checklist was uploaded but %d questions were still asked: %v", len(got), ids)
	}
}

func TestChecklistAbsentAsksTheSiteVisitQuestions(t *testing.T) {
	got := siteVisitQuestions(detected("EDR Historical Package", "Proposal / Contract"))
	if len(got) == 0 {
		t.Fatal("no checklist was uploaded and no questions were asked")
	}
	for _, q := range got {
		if strings.TrimSpace(q.Context) == "" {
			t.Errorf("question %q carries no Context note; the EP cannot tell why it appeared", q.ID)
		}
		// Every dynamic question must say why it is being asked AND how to
		// correct the trigger, so a wrong trigger reads as a signal rather than
		// as busywork.
		if !strings.Contains(q.Context, "No Site Recon Checklist") {
			t.Errorf("question %q does not state the absence that triggered it: %q", q.ID, q.Context)
		}
		if !strings.Contains(q.Context, "re-tag") {
			t.Errorf("question %q does not tell the EP how to correct a wrong trigger: %q", q.ID, q.Context)
		}
		if strings.TrimSpace(q.Answer) != "" {
			t.Errorf("question %q ships a pre-filled field observation %q", q.ID, q.Answer)
		}
	}
}

// No uploads at all is the absent case, not a special one.
func TestNoUploadsAsksTheSiteVisitQuestions(t *testing.T) {
	if len(siteVisitQuestions(nil)) == 0 {
		t.Error("an empty upload set suppressed the questions")
	}
}

// INDEPENDENT VERIFIER. The override vocabulary is checked against the
// CATEGORIZER'S OWN OUTPUTS, not against itself: a category the categorizer can
// produce but the EP cannot select is a file that can never be re-tagged back
// to its correct value.
func TestOverrideVocabularyCoversEveryCategorizerOutput(t *testing.T) {
	vocab := map[string]bool{}
	for _, c := range uploadCategories() {
		vocab[c] = true
	}

	// Filenames chosen to hit each branch from the outside, by the substrings a
	// real upload would carry rather than by reading the switch.
	probes := []string{
		"Signed Proposal 2026.pdf", "EDR Radius Map.pdf", "Aerial Photo Package.pdf",
		"Topo Quad.pdf", "Sanborn Set.pdf", "Filio Export.pdf", "Site Photos.pdf",
		"Site Recon Checklist.pdf", "Field Checklist.pdf", "Wetland Delineation.pdf",
		"FIRM Panel.pdf", "Flood Map.pdf", "VEC Application.pdf",
		"User Questionnaire.pdf", "Deed.pdf", "unlabelled scan.pdf",
	}
	for _, p := range probes {
		cat := categorizeUpload(p)
		if !vocab[cat] {
			t.Errorf("categorizeUpload(%q) = %q, which the EP cannot select in the override list", p, cat)
		}
	}
}

// The trigger and the categorizer must agree on the checklist category string.
// If they drift, the trigger silently never fires and five questions appear on
// every project that uploaded a checklist.
func TestCategorizerAndTriggerAgreeOnTheChecklistCategory(t *testing.T) {
	if got := categorizeUpload("Site Recon Checklist_Providence Road.pdf"); got != siteReconChecklistCategory {
		t.Fatalf("categorizeUpload = %q, trigger looks for %q", got, siteReconChecklistCategory)
	}
	if len(siteVisitQuestions(detected(categorizeUpload("recon checklist.pdf")))) != 0 {
		t.Error("the categorizer's own checklist output did not suppress the questions")
	}
}
