package main

import (
	"archive/zip"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/matrix-engineering/matrix-esa-agent/internal/core"
)

// parses reports whether s is well-formed XML with balanced elements.
func parses(t *testing.T, s string) error {
	t.Helper()
	dec := xml.NewDecoder(strings.NewReader(s))
	depth := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			if depth != 0 {
				return errUnbalanced(depth)
			}
			return nil
		}
		if err != nil {
			return err
		}
		switch tok.(type) {
		case xml.StartElement:
			depth++
		case xml.EndElement:
			depth--
		}
	}
}

type errUnbalanced int

func (e errUnbalanced) Error() string { return "unbalanced elements, final depth non-zero" }

// miniDocx writes a one-part docx whose document.xml is the given body, so the
// real mergeDocxLogic can run against it end to end.
func miniDocx(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "template.docx")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := zip.NewWriter(f)
	fw, err := w.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write([]byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + body)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func mergeToString(t *testing.T, body string, payload map[string]string) (string, error) {
	t.Helper()
	dir := t.TempDir()
	tpl := miniDocx(t, dir, body)
	out := filepath.Join(dir, "out.docx")
	js, _ := json.Marshal(payload)
	if err := mergeDocxLogic(tpl, js, out); err != nil {
		return "", err
	}
	r, err := zip.OpenReader(out)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	rc, err := r.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	b, _ := io.ReadAll(rc)
	return string(b), nil
}

func TestReplaceTagIsExactMatchOnly(t *testing.T) {
	// ParcelID is a prefix of SiteParcelID. The old bare-key fallback replaced
	// the substring anywhere, so whichever key Go's randomized map order
	// reached first corrupted the other -- differently on every run.
	xml := `<w:t>{{ParcelID}}</w:t><w:t>{{SiteParcelID}}</w:t>`
	got := replaceTag(xml, "ParcelID", "10-123-456")
	want := `<w:t>10-123-456</w:t><w:t>{{SiteParcelID}}</w:t>`
	if got != want {
		t.Fatalf("prefix collision:\n got %q\nwant %q", got, want)
	}
}

func TestReplaceTagOrderIndependent(t *testing.T) {
	// Applying the colliding keys in either order must converge on the same
	// document, which is what makes generation deterministic.
	const xml = `{{ParcelID}}|{{SiteParcelID}}`
	forward := replaceTag(replaceTag(xml, "ParcelID", "A"), "SiteParcelID", "B")
	reverse := replaceTag(replaceTag(xml, "SiteParcelID", "B"), "ParcelID", "A")
	if forward != reverse {
		t.Fatalf("order-dependent output: forward=%q reverse=%q", forward, reverse)
	}
	if forward != "A|B" {
		t.Fatalf("got %q, want %q", forward, "A|B")
	}
}

func TestReplaceTagAcceptsAlreadyWrappedKeys(t *testing.T) {
	// The compiler sometimes emits a key with its own braces attached.
	if got := replaceTag(`x {{SiteAcres}} y`, "{{SiteAcres}}", "1.7 Acres"); got != "x 1.7 Acres y" {
		t.Fatalf("wrapped key not normalized: %q", got)
	}
}

func TestFindUnreplacedTags(t *testing.T) {
	xml := `<w:t>{{Alpha}}</w:t> filled <w:t>{{Beta}}</w:t> <w:t>{{Alpha}}</w:t>`
	got := findUnreplacedTags(xml)
	want := []string{"Alpha", "Beta"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v (deduped and sorted)", got, want)
	}
	if len(findUnreplacedTags(`<w:t>all filled in</w:t>`)) != 0 {
		t.Fatal("clean document must report no unreplaced tags")
	}
}

func TestInjectFieldDefaultsCarriesNoClientHardcodes(t *testing.T) {
	// Every generated report was pinned to one client's details regardless of
	// the actual project. Guard against reintroduction.
	out := injectFieldDefaults(`{"SiteStreetAddress":"400 Providence Rd"}`, map[string]string{}, "")
	for _, banned := range []string{"Arkan", "Morningpark", "Hashem", "Gwinnett", "Roswell"} {
		if strings.Contains(out, banned) {
			t.Errorf("client hardcode %q leaked back into injectFieldDefaults output: %s", banned, out)
		}
	}
	if !strings.Contains(out, "400 Providence Rd") {
		t.Errorf("model-supplied address was overwritten: %s", out)
	}
}

func TestInjectFieldDefaultsAppliesEPAnswers(t *testing.T) {
	out := injectFieldDefaults(`{}`, map[string]string{
		"project_number": "MEG-302858",
		"parcel_id":      "11-0022-33",
		"site_acreage":   "4.2 Acres",
	}, "")
	// SiteAcres, not SiteAcreage: {{SiteAcres}} is the tag the template actually
	// contains, and the old spelling matched nothing at merge, so the EP's typed
	// acreage never reached the document.
	for _, want := range []string{`"ProjectNo":"302858"`, `"ParcelID":"11-0022-33"`, `"SiteAcres":"4.2 Acres"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in %s", want, out)
		}
	}
	for _, dead := range []string{"SiteAcreage", "site_acreage", "SiteParcelID", "parcel_id"} {
		if strings.Contains(out, `"`+dead+`"`) {
			t.Errorf("dead key %q written again: it is not a template tag and matches nothing at merge", dead)
		}
	}
}

// The inverse of the retired layout hack: model-supplied letter lines must
// survive. Blanking them unconditionally shipped Providence Road with an empty
// recipient block, so this guards against the hack being reinstated.
func TestInjectFieldDefaultsPreservesLetterRecipientBlock(t *testing.T) {
	out := injectFieldDefaults(
		`{"Proposal_Letter1":"Ms. Paige Singer","Proposal_Letter2":"DeKalb County Parks","Proposal_Letter3":"3681 Chestnut Street","Proposal_Letter4":"Scottdale, Georgia 30079"}`,
		map[string]string{}, "")
	for _, want := range []string{"Ms. Paige Singer", "DeKalb County Parks", "3681 Chestnut Street", "Scottdale, Georgia 30079"} {
		if !strings.Contains(out, want) {
			t.Errorf("letter recipient line %q was wiped: %s", want, out)
		}
	}
}

func TestUnfractureRejoinsBracesSplitAcrossRuns(t *testing.T) {
	// The exact shape found in the real template's header9/header10, where the
	// opening "{{" of {{ReportDate}} is two separate runs.
	in := `<w:t xml:space="preserve">   {</w:t></w:r><w:proofErr w:type="gramEnd"/><w:r><w:t>{</w:t></w:r>` +
		`<w:proofErr w:type="spellStart"/><w:r><w:t>ReportDate</w:t></w:r><w:proofErr w:type="spellEnd"/><w:r><w:t>}}</w:t>`
	got := unfractureDocxXML(in)
	if !strings.Contains(got, "{{ReportDate}}") {
		t.Fatalf("split braces were not rejoined into a complete tag: %s", got)
	}
	// And the rejoined tag must now be replaceable.
	filled := replaceTag(got, "ReportDate", "August 2026")
	if strings.Contains(filled, "{{") || strings.Contains(filled, "ReportDate") {
		t.Errorf("rejoined tag did not get replaced: %s", filled)
	}
}

func TestCountOrphanOpenBracesSeesFracturedSurvivors(t *testing.T) {
	// A complete tag is reported by findUnreplacedTags, not by the orphan count.
	if n := countOrphanOpenBraces(`<w:t>{{Complete}}</w:t>`); n != 0 {
		t.Errorf("complete tag counted as orphan: %d", n)
	}
	// An opening brace pair with no close is invisible to the tag list, which
	// is how the Providence Road run under-reported 15 unfilled tags as 13.
	if n := countOrphanOpenBraces(`<w:t>{{Dangling and no close</w:t>`); n != 1 {
		t.Errorf("orphan {{ not detected, got %d", n)
	}
	if n := countOrphanOpenBraces(`<w:t>{{A}} then {{Orphan</w:t>`); n != 1 {
		t.Errorf("mixed complete + orphan miscounted: %d", n)
	}
}

// --- XML safety -------------------------------------------------------------
//
// An unescaped "&" in a value such as "Diane & Brian J. Pete" produced a docx
// Word refused to open with "experienced an error trying to open the file".

func TestSanitizeDocxValueEscapesMarkupCharacters(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"ampersand", "Diane & Brian J. Pete", "Diane &amp; Brian J. Pete"},
		{"address ampersand", "12735 & 12725 Providence Road", "12735 &amp; 12725 Providence Road"},
		{"less than", "depth <5 feet", "depth &lt;5 feet"},
		{"greater than", "area >2 acres", "area &gt;2 acres"},
		{"all three", "A & B <C> D", "A &amp; B &lt;C&gt; D"},
		{"no double escape of literal text", "AT&T", "AT&amp;T"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := core.SanitizeDocxValue(c.in); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestSanitizeDocxValueConvertsNewlinesToBreaks(t *testing.T) {
	got := core.SanitizeDocxValue("First paragraph.\nSecond paragraph.")
	if !strings.Contains(got, "<w:br/>") {
		t.Fatalf("newline did not become a Word break: %q", got)
	}
	// CRLF must not produce two breaks.
	if n := strings.Count(core.SanitizeDocxValue("a\r\nb"), "<w:br/>"); n != 1 {
		t.Errorf("CRLF produced %d breaks, want 1", n)
	}
	// The break markup itself must survive escaping.
	if strings.Contains(got, "&lt;w:br") {
		t.Errorf("break markup was escaped: %q", got)
	}
}

func TestSanitizeDocxValueStripsIllegalControlChars(t *testing.T) {
	got := core.SanitizeDocxValue("clean\x00text\x07here\tkept")
	for _, bad := range []string{"\x00", "\x07"} {
		if strings.Contains(got, bad) {
			t.Errorf("illegal control char survived: %q", got)
		}
	}
	if !strings.Contains(got, "\t") {
		t.Errorf("tab is legal in XML 1.0 and must be kept: %q", got)
	}
}

func TestMergeProducesWellFormedXMLWithAmpersandValues(t *testing.T) {
	body := `<w:document><w:body><w:p><w:r><w:t>{{OwnerName}}</w:t></w:r></w:p></w:body></w:document>`
	got, err := mergeToString(t, body, map[string]string{"OwnerName": "Diane & Brian J. Pete"})
	if err != nil {
		t.Fatalf("merge failed: %v", err)
	}
	if err := parses(t, got); err != nil {
		t.Fatalf("merged document is not well-formed: %v\n%s", err, got)
	}
	if !strings.Contains(got, "Diane &amp; Brian J. Pete") {
		t.Errorf("value not escaped in output: %s", got)
	}
}

// The fractured case: Word splits {{OwnerName}} across runs, unfractureDocxXML
// rejoins it, and the replacement value itself contains an ampersand.
func TestMergeHandlesAmpersandAdjacentToFracturedTag(t *testing.T) {
	body := `<w:document><w:body><w:p>` +
		`<w:r><w:t>Owner: </w:t></w:r>` +
		`<w:r><w:t>{{Own</w:t></w:r><w:r><w:rPr><w:b/></w:rPr><w:t>erName}}</w:t></w:r>` +
		`<w:r><w:t> of record</w:t></w:r>` +
		`</w:p></w:body></w:document>`
	got, err := mergeToString(t, body, map[string]string{"OwnerName": "Diane & Brian J. Pete"})
	if err != nil {
		t.Fatalf("merge failed: %v", err)
	}
	if err := parses(t, got); err != nil {
		t.Fatalf("fractured-tag merge is not well-formed: %v\n%s", err, got)
	}
	if !strings.Contains(got, "Diane &amp; Brian J. Pete") {
		t.Errorf("fractured tag with ampersand value not filled correctly: %s", got)
	}
	if strings.Contains(got, "{{") || strings.Contains(got, "}}") {
		t.Errorf("braces survived the merge: %s", got)
	}
}

// TestMergeRealTemplateWithHazardousValues runs the real merge against the real
// firm template using the exact values that broke the Providence Road artifact.
// Skips when the template is absent (it is not in a clean checkout's path on
// every machine), so this never becomes a spurious CI failure.
func TestMergeRealTemplateWithHazardousValues(t *testing.T) {
	tpl := filepath.Join("..", "..", "knowledge", "ESA_PHASE_I_Template.docx")
	if _, err := os.Stat(tpl); err != nil {
		tpl = filepath.Join("..", "..", "ESA_PHASE_I_BLANK_TEMPLATE.docx")
		if _, err := os.Stat(tpl); err != nil {
			t.Skip("no template available; skipping real-template integration check")
		}
	}

	payload := map[string]string{
		"OwnerName":             "Medina on Providence LLC and Diane & Brian J. Pete",
		"SiteStreetAddress":     "12735 & 12725 Providence Road and 272 Lantern Ridge Court",
		"Opinions_Text":         "First paragraph.\nSecond paragraph.\r\nThird after CRLF.",
		"DataGaps_Text":         "Depth <5 feet; area >2 acres",
		"ExecutiveSummary_Text": "Control chars \x00\x07 stripped\tbut tabs kept.",
		// Fractured across runs in header9/header10 -- survived into the
		// delivered Providence Road document.
		"ReportDate": "August 2026",
	}
	js, _ := json.Marshal(payload)
	out := filepath.Join(t.TempDir(), "real.docx")

	// mergeDocxLogic runs post-merge validation internally, so a nil error here
	// means every rewritten part re-parsed clean.
	if err := mergeDocxLogic(tpl, js, out); err != nil {
		t.Fatalf("real-template merge failed: %v", err)
	}
	if err := core.ValidateDocxXML(out); err != nil {
		t.Fatalf("real-template output failed validation: %v", err)
	}

	// The fractured header tag must actually be filled now.
	r, err := zip.OpenReader(out)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	headersChecked := 0
	for _, f := range r.File {
		if !strings.HasPrefix(f.Name, "word/header") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		s := string(b)
		if strings.Contains(s, "ReportDate") {
			t.Errorf("%s still contains the ReportDate placeholder after merge", f.Name)
		}
		if n := countOrphanOpenBraces(s); n > 0 {
			t.Errorf("%s has %d orphan open braces after merge", f.Name, n)
		}
		headersChecked++
	}
	if headersChecked == 0 {
		t.Error("no header parts found; the fractured-tag check did not run")
	}
}

// The running header is deterministic: template formatting plus Go-supplied
// values. The Providence Road draft shipped stamped "July 29, 2026" from an
// August 5 run because ReportDate was left to the Template Compiler.
func TestInjectFieldDefaultsOverwritesModelReportDate(t *testing.T) {
	want := core.ReportDateNow()

	for _, modelKey := range []string{"ReportDate", "{{ReportDate}}"} {
		t.Run(modelKey, func(t *testing.T) {
			payload := fmt.Sprintf(`{%q: "July 29, 2026"}`, modelKey)

			var got map[string]interface{}
			if err := json.Unmarshal([]byte(injectFieldDefaults(payload, nil, "")), &got); err != nil {
				t.Fatalf("injectFieldDefaults produced invalid JSON: %v", err)
			}
			if got["ReportDate"] != want {
				t.Errorf("ReportDate = %v, want %v — the model's date must always be overwritten", got["ReportDate"], want)
			}
		})
	}
}

func TestInjectFieldDefaultsSetsReportDateWhenModelOmitsIt(t *testing.T) {
	var got map[string]interface{}
	if err := json.Unmarshal([]byte(injectFieldDefaults(`{"OwnerName":"X"}`, nil, "")), &got); err != nil {
		t.Fatalf("injectFieldDefaults produced invalid JSON: %v", err)
	}
	if got["ReportDate"] != core.ReportDateNow() {
		t.Errorf("ReportDate = %v, want %v — an absent model key must still yield a dated header",
			got["ReportDate"], core.ReportDateNow())
	}
}

// The DraftNote key must always be emitted. An unset key leaves a literal
// "{{DraftNote}}" on the cover page of every report with a complete baseline.
func TestInjectFieldDefaultsAlwaysEmitsDraftNote(t *testing.T) {
	cases := map[string]string{
		"complete baseline": "",
		"partial baseline":  "[DRAFT NOTE — INTERNAL: generated against 18 of 19 historical baselines — remove before issuance]",
	}

	for name, note := range cases {
		t.Run(name, func(t *testing.T) {
			var got map[string]interface{}
			if err := json.Unmarshal([]byte(injectFieldDefaults(`{}`, nil, note)), &got); err != nil {
				t.Fatalf("invalid JSON: %v", err)
			}
			v, present := got["DraftNote"]
			if !present {
				t.Fatal("DraftNote key absent; the placeholder would survive into the document")
			}
			if v != note {
				t.Errorf("DraftNote = %q, want %q", v, note)
			}
		})
	}
}

// End to end against the real template: the cover placeholder is filled in both
// directions, and an empty note leaves no visible residue.
func TestRealTemplateDraftNoteRendersOnCover(t *testing.T) {
	tpl := filepath.Join("..", "..", "knowledge", "ESA_PHASE_I_Template.docx")
	if _, err := os.Stat(tpl); err != nil {
		t.Skip("firm template not available; skipping draft note check")
	}

	note := "[DRAFT NOTE — INTERNAL: generated against 18 of 19 historical baselines — remove before issuance]"
	for _, tc := range []struct{ name, value string }{
		{"incomplete baseline shows the note", note},
		{"complete baseline shows nothing", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			js := []byte(injectFieldDefaults(`{}`, nil, tc.value))
			out := filepath.Join(t.TempDir(), "draftnote.docx")
			if err := mergeDocxLogic(tpl, js, out); err != nil {
				t.Fatalf("merge failed: %v", err)
			}

			r, err := zip.OpenReader(out)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			for _, f := range r.File {
				if f.Name != "word/document.xml" {
					continue
				}
				rc, _ := f.Open()
				b, _ := io.ReadAll(rc)
				rc.Close()
				s := string(b)

				if strings.Contains(s, "DraftNote") {
					t.Error("the {{DraftNote}} placeholder survived the merge")
				}
				text := visibleText(s)
				if tc.value == "" {
					if strings.Contains(text, "DRAFT NOTE") {
						t.Error("a complete baseline still rendered a draft note")
					}
				} else if !strings.Contains(text, "DRAFT NOTE — INTERNAL") {
					t.Error("the draft note did not render on the cover")
				}
			}
		})
	}
}

// Guards the header repair in knowledge/ESA_PHASE_I_Template.docx: the date and
// project number are positioned by a right-aligned tab stop, not by runs of
// literal spaces, and "MEG  Project No." keeps its intentional double space.
func TestRealTemplateHeaderIsCleanlyPositioned(t *testing.T) {
	tpl := filepath.Join("..", "..", "knowledge", "ESA_PHASE_I_Template.docx")
	if _, err := os.Stat(tpl); err != nil {
		t.Skip("firm template not available; skipping header layout check")
	}

	js, _ := json.Marshal(map[string]string{
		"ReportDate":        "August 11, 2026",
		"SiteStreetAddress": "12725 Providence Rd",
		"ProjectNo":         "303315",
	})
	out := filepath.Join(t.TempDir(), "header.docx")
	if err := mergeDocxLogic(tpl, js, out); err != nil {
		t.Fatalf("merge failed: %v", err)
	}

	r, err := zip.OpenReader(out)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	checked := 0
	for _, f := range r.File {
		if !strings.HasPrefix(f.Name, "word/header") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		s := string(b)

		if !strings.Contains(s, "MEG  Project No.") {
			continue // not one of the two running-header parts
		}
		checked++

		text := visibleText(s)
		if strings.Contains(text, "    ") {
			t.Errorf("%s: header still positions content with runs of literal spaces: %q", f.Name, text)
		}
		if !strings.Contains(s, `<w:tab w:val="right"`) {
			t.Errorf("%s: header lost its right-aligned tab stop", f.Name)
		}
		if strings.Contains(s, "proofErr") {
			t.Errorf("%s: proofErr markers survived; the grammar squiggle under the project number returns", f.Name)
		}
		if !strings.Contains(s, "<w:noProof/>") {
			t.Errorf("%s: the MEG run lost <w:noProof/>, so Word will squiggle the intentional double space", f.Name)
		}
		for _, want := range []string{"August 11, 2026", "12725 Providence Rd", "MEG  Project No. 303315"} {
			if !strings.Contains(text, want) {
				t.Errorf("%s: header missing %q; got %q", f.Name, want, text)
			}
		}
	}
	if checked == 0 {
		t.Error("no running-header part found; the layout check did not run")
	}
}

// visibleText concatenates the <w:t> content of a document part.
func visibleText(xml string) string {
	var sb strings.Builder
	for rest := xml; ; {
		i := strings.Index(rest, "<w:t")
		if i < 0 {
			break
		}
		rest = rest[i:]
		open := strings.Index(rest, ">")
		if open < 0 {
			break
		}
		close := strings.Index(rest, "</w:t>")
		if close < 0 {
			break
		}
		sb.WriteString(rest[open+1 : close])
		rest = rest[close+len("</w:t>"):]
	}
	return sb.String()
}

func TestMergeRejectsMalformedOutput(t *testing.T) {
	// A template that is already broken must fail the post-merge validation
	// rather than being written out and reported as a success.
	body := `<w:document><w:body><w:p><w:t>{{X}}</w:t></w:p></w:body>` // unclosed w:document
	_, err := mergeToString(t, body, map[string]string{"X": "value"})
	if err == nil {
		t.Fatal("malformed output was accepted; post-merge validation did not fire")
	}
	if !strings.Contains(err.Error(), "post-merge validation") {
		t.Errorf("expected a post-merge validation error, got: %v", err)
	}
}
