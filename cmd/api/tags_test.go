package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func realTemplate(t *testing.T) string {
	t.Helper()
	tpl := filepath.Join("..", "..", "knowledge", "ESA_PHASE_I_Template.docx")
	if _, err := os.Stat(tpl); err != nil {
		t.Skip("firm template not available; skipping canonical inventory check")
	}
	return tpl
}

func TestParseCanonicalTagsFromRealTemplate(t *testing.T) {
	inv, err := ParseCanonicalTags(realTemplate(t))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if len(inv.All) != len(inv.Slots)+len(inv.Substantive) {
		t.Errorf("classification lost tags: %d total vs %d slots + %d substantive",
			len(inv.All), len(inv.Slots), len(inv.Substantive))
	}
	// UpN_* 1-14 x 5 fields, DownN_* 1-20 x 5 fields.
	if len(inv.Slots) != 170 {
		t.Errorf("slots = %d, want 170", len(inv.Slots))
	}
	// The union of body, headers and footers. Body alone is 299; ReportDate
	// lives only in header9/header10, and counting the body alone would
	// re-prompt the model for a header tag it was never asked to supply.
	if !inv.Has("ReportDate") {
		t.Error("ReportDate missing: header parts were not included in the union")
	}
	if !inv.Has("DraftNote") {
		t.Error("DraftNote missing from the canonical list")
	}
	for _, tag := range []string{"Sec8_13_Radon", "Sec8_15_LBP", "Sec9_Item1_SiteInfo", "GWFlowDir"} {
		if !inv.Has(tag) {
			t.Errorf("expected canonical tag %q not found", tag)
		}
	}
	if inv.IsSlot("Sec9_Item1_SiteInfo") {
		t.Error("Sec9_Item1_SiteInfo misclassified as a table slot")
	}
	if !inv.IsSlot("Up1_Name") || !inv.IsSlot("Down20_Class") {
		t.Error("UpN_/DownN_ tags not classified as slots")
	}
}

// The parser must join <w:t> runs with NO separator. Joining with a space turns
// a tag Word split across runs into a phantom "corrupted" name -- a mistake that
// produced a list of 17 nonexistent broken tags during design.
func TestDocxPartTextJoinsRunsWithoutSeparator(t *testing.T) {
	// The real shape: Word split Sec8_10_Spills across three runs.
	fractured := `<w:r><w:t>{{Sec8_</w:t></w:r><w:r><w:t>10</w:t></w:r><w:r><w:t>_Spills}}</w:t></w:r>`

	got := docxPartText(fractured)
	if got != "{{Sec8_10_Spills}}" {
		t.Fatalf("docxPartText = %q, want %q (runs must join with no separator)", got, "{{Sec8_10_Spills}}")
	}
	if strings.Contains(got, " ") {
		t.Error("a separator leaked into the joined text; tag names would be corrupted")
	}
	if m := tagPattern.FindAllStringSubmatch(got, -1); len(m) != 1 || m[1-1][1] != "Sec8_10_Spills" {
		t.Errorf("tag not recoverable from joined text: %v", m)
	}
}

func TestParseCanonicalTagsRejectsTaglessTemplate(t *testing.T) {
	dir := t.TempDir()
	tpl := miniDocx(t, dir, `<w:document><w:body><w:p><w:r><w:t>no tags here</w:t></w:r></w:p></w:body></w:document>`)

	if _, err := ParseCanonicalTags(tpl); err == nil {
		t.Error("a template with no tags must be an error: it means the wrong file or a broken container")
	}
}

// --- drift check --------------------------------------------------------------

const driftFixture = "" +
	"6. **Section 8.13 Radon (`{{Sec8_13_Radon}}`)**:\n" +
	"   - Some prose mentioning `{{GhostProseTag}}` in passing.\n" +
	"7. **A rule pointing at nothing (`{{GhostHeadingTag}}`)**:\n" +
	"   - A deliberate historical note about `{{RetiredTag}}` " + driftIgnoreMarker + "\n" +
	"\n```json\n" +
	"{\n" +
	`  "{{Sec9_Item1_SiteInfo}}": "string",` + "\n" +
	`  "{{GhostSchemaTag}}": "string"` + "\n" +
	"}\n" +
	"```\n"

func TestCheckSkillTagDriftClassifiesByBindingness(t *testing.T) {
	inv := testInventory(t, "Sec8_13_Radon", "Sec9_Item1_SiteInfo")
	report := CheckSkillTagDrift(inv, "fixture/SKILL.md", driftFixture)

	if len(report.BindingHeadings) != 1 || report.BindingHeadings[0] != "GhostHeadingTag" {
		t.Errorf("BindingHeadings = %v, want [GhostHeadingTag]", report.BindingHeadings)
	}
	// The schema-rivalry class: a dead key beside its live twin, invisible to a
	// headings-only scan. It survived two delivered reports.
	if len(report.BindingSchema) != 1 || report.BindingSchema[0] != "GhostSchemaTag" {
		t.Errorf("BindingSchema = %v, want [GhostSchemaTag]", report.BindingSchema)
	}
	if len(report.Prose) != 1 || report.Prose[0] != "GhostProseTag" {
		t.Errorf("Prose = %v, want [GhostProseTag] (RetiredTag carries the ignore marker)", report.Prose)
	}
	if !report.HasBinding() {
		t.Error("HasBinding should be true")
	}
}

func TestCheckSkillTagDriftQuietWhenClean(t *testing.T) {
	inv := testInventory(t, "Sec8_13_Radon", "Sec9_Item1_SiteInfo", "GhostProseTag",
		"GhostHeadingTag", "GhostSchemaTag")
	if report := CheckSkillTagDrift(inv, "fixture/SKILL.md", driftFixture); !report.Empty() {
		t.Errorf("clean skill reported drift: %+v", report)
	}
}

// The schema block must be clean right now: ca08ead removed the dead keys, and
// this tier fails boot. If a dead schema key returns, this catches it before a
// deploy does.
func TestRealCompilerSkillHasNoSchemaDrift(t *testing.T) {
	inv, err := ParseCanonicalTags(realTemplate(t))
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join("..", "..", skillTemplate))
	if err != nil {
		t.Skipf("compiler skill not readable: %v", err)
	}
	report := CheckSkillTagDrift(inv, skillTemplate, string(content))

	if len(report.BindingSchema) > 0 {
		t.Errorf("template-compiler JSON schema names tags the template does not have: %v\n"+
			"This is the schema-rivalry class: the model may fill either the dead key or its live twin, "+
			"so the same bug produces a blank on one run and content on the next.", report.BindingSchema)
	}
	// Both binding tiers now fail boot, so both have to stay clean.
	if len(report.BindingHeadings) > 0 {
		t.Errorf("template-compiler rule headings name tags the template does not have: %v\n"+
			"A rule target pointing at nothing is a rule written against nothing — Section 8.13 "+
			"shipped empty that way. This fails boot.", report.BindingHeadings)
	}
}
