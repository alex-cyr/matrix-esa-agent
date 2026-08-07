package main

import (
	"archive/zip"
	"encoding/json"
	"encoding/xml"
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
	out := injectFieldDefaults(`{"SiteStreetAddress":"400 Providence Rd"}`, map[string]string{})
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
	})
	for _, want := range []string{`"ProjectNo":"302858"`, `"ParcelID":"11-0022-33"`, `"SiteAcreage":"4.2 Acres"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in %s", want, out)
		}
	}
	// The cover-letter text boxes must stay blanked (page-2 layout hack).
	if !strings.Contains(out, `"Proposal_Letter1":""`) {
		t.Errorf("Proposal_Letter blanking lost: %s", out)
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
