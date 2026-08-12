package main

import (
	"strings"
	"testing"
)

func signals(findings []SpliceFinding) []string {
	var out []string
	for _, f := range findings {
		out = append(out, f.Signal)
	}
	return out
}

func hasSignal(findings []SpliceFinding, want string) bool {
	for _, f := range findings {
		if f.Signal == want {
			return true
		}
	}
	return false
}

// Signal B, the case that motivated the detector. Literal overlap is only two
// words ("the topography"), so the originally specified 5-word rule -- and
// signal A at 3 -- would both miss it.
func TestSpliceDetectsTopographyRestatement(t *testing.T) {
	before := "Based on the topographical information obtained from USGS Historical Topographic Maps, the topography of the site "
	value := "The topography suggests the site slopes to the east."
	after := ". Site reconnaissance revealed"

	got := detectSplice("word/document.xml", "USGS_TopoSummary", before, value, after)
	if !hasSignal(got, "sentence-start") {
		t.Fatalf("the delivered Providence splice was not detected; signals=%v", signals(got))
	}
}

// Signal B refinement, pinned in both directions with cases from the first
// acceptance run. The unrefined rule produced 73 findings across 67 tags,
// almost all proper nouns; the lexical-echo condition silences those without
// losing the one catch that mattered.
func TestSpliceSentenceStartRequiresLexicalEcho(t *testing.T) {
	// Both true positives observed in live runs. Neither may be lost to a
	// noise-reduction change.
	fires := []struct{ name, before, value, after string }{
		{
			"topography restatement still fires",
			"Based on the topographical information obtained from USGS Historical Topographic Maps, the topography of the site ",
			"The topography suggests the site slopes to the east.", ".",
		},
		{
			"site-is-presently restatement still fires",
			"The site was accessed from Providence Road via a gravel drive. The site is presently ",
			"The site is currently developed with two single-family residences.", ".",
		},
	}
	for _, tc := range fires {
		t.Run(tc.name, func(t *testing.T) {
			if got := detectSplice("p", "USGS_TopoSummary", tc.before, tc.value, tc.after); !hasSignal(got, "sentence-start") {
				t.Errorf("expected sentence-start; got %v", signals(got))
			}
		})
	}

	// Every one of these fired before the refinement.
	silent := []struct{ name, tag, before, value, after string }{
		{"city/state/zip", "SiteCityStateZip", "Environmental Site Assessment - Phase I At 272 Lantern Ridge Court ", "Alpharetta, GA 30009", ""},
		{"recipient name", "Proposal_To1", "Submitted to ", "Mr. Ihssan Hashem", ""},
		{"client entity", "User_ClientName", "appreciates the opportunity to work with ", "Arkan Homes LLC", " on this project"},
		{"month and year", "ReportMonthYear", "Project Number MEG 303315 ", "August 2026", ""},
		{"county name", "SiteCounty", "The subject site is located at 272 Lantern Ridge Court. According to the ", "Fulton", " County Tax Assessor"},
		// The eight remaining false positives from the confirmation run: a
		// one-word table cell whose value echoes nearby template prose.
		{"adjacent-use table cell", "North_AdjUse",
			"Surrounding properties consist primarily of residential use as tabulated below: Direction Adjacent Properties Surrounding Properties North ",
			"Residential", ""},
	}
	for _, tc := range silent {
		t.Run(tc.name, func(t *testing.T) {
			if got := detectSplice("p", tc.tag, tc.before, tc.value, tc.after); hasSignal(got, "sentence-start") {
				t.Errorf("proper noun flagged as a splice: %v", signals(got))
			}
		})
	}
}

// The correct fragment must stay silent.
func TestSpliceQuietOnCorrectFragment(t *testing.T) {
	before := "Based on the topographical information obtained from USGS Historical Topographic Maps, the topography of the site "
	value := "gently slopes to the east toward an unnamed tributary"
	after := ". 7.2 Site Geology"

	if got := detectSplice("word/document.xml", "USGS_TopoSummary", before, value, after); len(got) > 0 {
		t.Errorf("clean continuation flagged: %v", signals(got))
	}
}

// Signal A: a longer restatement of the lead-in.
func TestSpliceDetectsLeadInOverlap(t *testing.T) {
	before := "Matrix was authorized to perform this work under "
	value := "Matrix was authorized to perform this work under a signed proposal dated July 6, 2026"
	after := "."

	got := detectSplice("word/document.xml", "User_Authorization", before, value, after)
	if !hasSignal(got, "overlap") {
		t.Fatalf("lead-in restatement not detected; signals=%v", signals(got))
	}
}

// THE BRACKET EXEMPTION. [EP VERIFY: ...] and [MEG DATAGAP: ...] are designed to
// land mid-sentence and begin with a capital -- the GWFlowDir slot guarantees
// it. Without the exemption the detector fires on every correctly flagged
// report and gets switched off.
func TestSpliceExemptsOurOwnBrackets(t *testing.T) {
	before := "shallow groundwater flow generally follows the topography of the land surface, and on this basis, groundwater is inferred to flow in a "
	after := " direction. 7.4 Wetlands"

	for _, value := range []string{
		"[EP VERIFY: groundwater flow direction]",
		"[MEG DATAGAP: groundwater flow direction]",
		"[MEG DATAGAP: Sec9_Item3_Wetlands]",
	} {
		t.Run(value, func(t *testing.T) {
			got := detectSplice("word/document.xml", "GWFlowDir", before, value, after)
			if hasSignal(got, "sentence-start") {
				t.Errorf("bracket value flagged as a mid-sentence splice: %v", signals(got))
			}
		})
	}
}

// A settled directional value in the same slot is also clean.
func TestSpliceQuietOnBareDirection(t *testing.T) {
	before := "groundwater is inferred to flow in a "
	value := "easterly"
	after := " direction."

	if got := detectSplice("word/document.xml", "GWFlowDir", before, value, after); len(got) > 0 {
		t.Errorf("bare directional adjective flagged: %v", signals(got))
	}
}

// Signal C, from the delivered Providence report: the template reads
// "occupies approximately {{SiteAcres}} acres in size" and the value carried
// its own unit, delivering "1.7 Acres acres".
func TestSpliceDetectsDuplicatedUnit(t *testing.T) {
	before := "the subject site occupies approximately "
	value := "1.7 Acres"
	after := " acres in size (Parcel ID 10-123-456)"

	got := detectSplice("word/document.xml", "SiteAcres", before, value, after)
	if !hasSignal(got, "duplicate-word") {
		t.Fatalf("duplicated unit not detected; signals=%v", signals(got))
	}
}

// Case must not matter: the duplication is a duplication whether the value
// says "Acres", "ACRES" or "acres". normalizeWords lowercases, and this pins
// that behaviour so a future change to normalization cannot quietly lose it.
func TestSpliceDuplicateWordIsCaseInsensitive(t *testing.T) {
	for _, value := range []string{"1.7 Acres", "1.7 ACRES", "1.7 acres"} {
		t.Run(value, func(t *testing.T) {
			got := detectSplice("word/document.xml", "SiteAcres",
				"the subject site occupies approximately ", value, " acres in size")
			if !hasSignal(got, "duplicate-word") {
				t.Errorf("case variant %q not detected; signals=%v", value, signals(got))
			}
		})
	}
}

func TestSpliceQuietOnBareNumber(t *testing.T) {
	before := "the subject site occupies approximately "
	value := "1.7"
	after := " acres in size"

	if got := detectSplice("word/document.xml", "SiteAcres", before, value, after); len(got) > 0 {
		t.Errorf("correct bare number flagged: %v", signals(got))
	}
}

func TestSpliceDetectsDoubledPeriod(t *testing.T) {
	before := "This work was performed in accordance with "
	value := "a signed proposal dated July 6, 2026."
	after := ". Matrix appreciates"

	got := detectSplice("word/document.xml", "User_Authorization", before, value, after)
	if !hasSignal(got, "doubled-period") {
		t.Fatalf("doubled period not detected; signals=%v", signals(got))
	}
}

// A capitalised value is correct where the template has closed the sentence.
func TestSpliceQuietWhenTemplateEndedTheSentence(t *testing.T) {
	before := "reviewed for the Subject Property. "
	value := "No historical industrial activities were depicted."
	after := " 5.3 Topographic Maps"

	if got := detectSplice("word/document.xml", "Sanborn_Summary", before, value, after); len(got) > 0 {
		t.Errorf("standalone sentence after a full stop flagged: %v", signals(got))
	}
}

func TestSpliceIgnoresEmptyValue(t *testing.T) {
	if got := detectSplice("p", "T", "the site ", "", " acres"); len(got) > 0 {
		t.Errorf("empty value produced findings: %v", signals(got))
	}
}

// --- empty numbered paragraph removal ------------------------------------------

const numberedPara = `<w:p w14:paraId="A"><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="8"/></w:numPr></w:pPr><w:r><w:t>%s</w:t></w:r></w:p>`

func TestRemovesEmptyNumberedParagraph(t *testing.T) {
	xml := `<w:body>` +
		strings.Replace(numberedPara, "%s", "", 1) +
		strings.Replace(numberedPara, "%s", "A real finding.", 1) +
		`</w:body>`

	got, n := removeEmptyNumberedParagraphs(xml)
	if n != 1 {
		t.Fatalf("removed %d paragraphs, want 1", n)
	}
	if !strings.Contains(got, "A real finding.") {
		t.Error("the populated numbered paragraph was removed")
	}
	if strings.Count(got, "<w:p ") != 1 {
		t.Errorf("expected exactly one paragraph left: %s", got)
	}
}

// Guard 1: paragraphs without numbering are never touched, however empty.
func TestKeepsEmptyParagraphWithoutNumbering(t *testing.T) {
	xml := `<w:body><w:p w14:paraId="B"><w:pPr><w:jc w:val="center"/></w:pPr></w:p></w:body>`
	got, n := removeEmptyNumberedParagraphs(xml)
	if n != 0 || got != xml {
		t.Errorf("an unnumbered empty paragraph was removed; spacing paragraphs are load-bearing on the cover")
	}
}

// Guard 2: a numbered paragraph whose tag failed to fill still contains
// "{{Tag}}" and must survive, so the unreplaced-tag report can still see it.
func TestKeepsNumberedParagraphWithUnreplacedTag(t *testing.T) {
	xml := `<w:body>` + strings.Replace(numberedPara, "%s", "{{Sec9_Item4_Flood}}", 1) + `</w:body>`
	if got, n := removeEmptyNumberedParagraphs(xml); n != 0 || got != xml {
		t.Error("a paragraph holding an unreplaced tag was removed, hiding the failure")
	}
}

// Guard 3: non-text content keeps a paragraph alive even with no <w:t>.
func TestKeepsNumberedParagraphWithDrawing(t *testing.T) {
	xml := `<w:body><w:p w14:paraId="C"><w:pPr><w:numPr><w:numId w:val="8"/></w:numPr></w:pPr>` +
		`<w:r><w:drawing><wp:inline/></w:drawing></w:r></w:p></w:body>`
	if _, n := removeEmptyNumberedParagraphs(xml); n != 0 {
		t.Error("a numbered paragraph containing a drawing was removed")
	}
}

func TestRemovalLeavesCleanDocumentWhenNothingEmpty(t *testing.T) {
	xml := `<w:body>` + strings.Replace(numberedPara, "%s", "Finding one.", 1) + `</w:body>`
	if got, n := removeEmptyNumberedParagraphs(xml); n != 0 || got != xml {
		t.Error("a fully populated document was altered")
	}
}

// --- end to end through the merge -----------------------------------------------

func TestMergeRemovesEmptyNumberedSlotEndToEnd(t *testing.T) {
	body := `<w:document><w:body>` +
		strings.Replace(numberedPara, "%s", "{{Sec9_Item1_SiteInfo}}", 1) +
		strings.Replace(numberedPara, "%s", "{{Sec9_Item2_Topo}}", 1) +
		`</w:body></w:document>`

	got, err := mergeToString(t, body, map[string]string{
		"Sec9_Item1_SiteInfo": "The site is a vacant parcel.",
		"Sec9_Item2_Topo":     "", // deliberately empty
	})
	if err != nil {
		t.Fatalf("merge failed: %v", err)
	}
	if !strings.Contains(got, "The site is a vacant parcel.") {
		t.Error("populated finding lost")
	}
	if strings.Count(got, "<w:p ") != 1 {
		t.Errorf("empty numbered slot not removed: %s", got)
	}
}
