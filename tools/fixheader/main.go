// fix_header_template rewrites the two running-header paragraphs in
// knowledge/ESA_PHASE_I_Template.docx (word/header9.xml and word/header10.xml)
// so the header is deterministic layout rather than hand-nudged whitespace.
//
// What it repairs, all three seen in the delivered Providence Road report:
//
//   - Doubled spacing in the date cell. The date was pushed rightward by seven
//     tab runs plus ten literal spaces spread over three runs. Replaced with a
//     single right-aligned tab stop at the right margin (9360 twips = 12240
//     page width - 2 x 1440 margin).
//   - The "stray underline" under the project number. There is no <w:u> run
//     anywhere in either header part; the mark is Word's grammar squiggle,
//     which the <w:proofErr w:type="gramStart"/> pair brackets around exactly
//     "MEG  Project". The intentional double space in the house format is what
//     trips the grammar checker. Fixed by carrying <w:noProof/> on that run --
//     which suppresses it permanently -- rather than by removing the space.
//   - The fractured {{ of {{ReportDate}}, split across two runs. unfractureDocxXML
//     repairs this at merge time, but the template should not need repairing.
//
// Run from the repo root:
//
//	go run ./tools/fixheader
//
// Idempotent: re-running finds the paragraphs already clean and reports no
// change. It lives in tools/ rather than scratch/ because scratch/ is
// gitignored, and a repair applied to a committed binary .docx is otherwise
// unreviewable — this file is the diff.
package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/matrix-engineering/matrix-esa-agent/internal/core"
)

const templatePath = "knowledge/ESA_PHASE_I_Template.docx"

// Right-aligned tab stop at the right margin. Section geometry from
// document.xml: <w:pgSz w:w="12240"/> with left and right margins of 1440.
const rightTabTwips = "9360"

var headerParts = map[string]bool{
	"word/header9.xml":  true,
	"word/header10.xml": true,
}

// The rebuilt paragraph bodies. The <w:p ...> opening tag of the original is
// preserved so paraId/rsid attributes survive.
const (
	titleLineBody = `<w:pPr><w:pStyle w:val="Heading1"/><w:tabs><w:tab w:val="right" w:pos="` + rightTabTwips + `"/></w:tabs></w:pPr>` +
		`<w:r><w:t>Environmental Site Assessment - Phase I</w:t></w:r>` +
		`<w:r><w:tab/></w:r>` +
		`<w:r><w:rPr><w:noProof/></w:rPr><w:t>{{ReportDate}}</w:t></w:r>`

	// "MEG  Project No." keeps its double space: intentional house formatting,
	// not a defect. <w:noProof/> is what stops Word drawing a grammar squiggle
	// under it.
	siteLineBody = `<w:pPr><w:pStyle w:val="Heading1"/><w:tabs><w:tab w:val="right" w:pos="` + rightTabTwips + `"/></w:tabs></w:pPr>` +
		`<w:r><w:t>{{SiteStreetAddress}}</w:t></w:r>` +
		`<w:r><w:tab/></w:r>` +
		`<w:r><w:rPr><w:noProof/></w:rPr><w:t xml:space="preserve">MEG  Project No. {{ProjectNo}}</w:t></w:r>`
)

func main() {
	if _, err := os.Stat(templatePath); err != nil {
		log.Fatalf("template not found at %s (run from the repo root): %v", templatePath, err)
	}

	r, err := zip.OpenReader(templatePath)
	if err != nil {
		log.Fatalf("open template: %v", err)
	}

	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	changed := map[string]bool{}

	for _, f := range r.File {
		rc, err := f.Open()
		if err != nil {
			log.Fatalf("open %s: %v", f.Name, err)
		}
		content, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			log.Fatalf("read %s: %v", f.Name, err)
		}

		if headerParts[f.Name] {
			rewritten, err := rewriteHeader(string(content))
			if err != nil {
				log.Fatalf("%s: %v", f.Name, err)
			}
			if rewritten != string(content) {
				changed[f.Name] = true
			}
			content = []byte(rewritten)
		}

		hdr := f.FileHeader // copy; the writer recomputes sizes and CRC
		fw, err := w.CreateHeader(&hdr)
		if err != nil {
			log.Fatalf("create %s: %v", f.Name, err)
		}
		if _, err := fw.Write(content); err != nil {
			log.Fatalf("write %s: %v", f.Name, err)
		}
	}
	r.Close()
	if err := w.Close(); err != nil {
		log.Fatalf("close zip: %v", err)
	}

	if len(changed) == 0 {
		fmt.Println("no change — header parts are already clean")
		return
	}

	// Write beside the template and validate before replacing it, so a bad
	// rewrite cannot destroy the only copy of the template.
	tmpPath := templatePath + ".rewrite"
	if err := os.WriteFile(tmpPath, buf.Bytes(), 0o644); err != nil {
		log.Fatalf("write %s: %v", tmpPath, err)
	}
	if err := core.ValidateDocxXML(tmpPath); err != nil {
		log.Fatalf("rewritten template failed XML validation, original left untouched: %v", err)
	}
	if err := os.Rename(tmpPath, templatePath); err != nil {
		log.Fatalf("replace template: %v", err)
	}

	for name := range changed {
		fmt.Printf("rewrote %s\n", name)
	}
	fmt.Printf("%s validated and updated\n", templatePath)
}

// rewriteHeader replaces the whole <w:p>...</w:p> block containing each header
// tag. Locating by enclosing paragraph rather than by matching the exact run
// soup means the tool does not depend on the current fracture pattern.
func rewriteHeader(xml string) (string, error) {
	out, err := replaceParagraphContaining(xml, ">ReportDate<", "{{ReportDate}}", titleLineBody)
	if err != nil {
		return "", err
	}
	return replaceParagraphContaining(out, ">ProjectNo<", "{{ProjectNo}}", siteLineBody)
}

// replaceParagraphContaining finds the paragraph holding needle (or, if the
// part has already been rewritten, cleanNeedle) and swaps its body for newBody,
// keeping the original <w:p ...> opening tag.
func replaceParagraphContaining(xml, needle, cleanNeedle, newBody string) (string, error) {
	idx := strings.Index(xml, needle)
	if idx < 0 {
		if idx = strings.Index(xml, cleanNeedle); idx < 0 {
			return "", fmt.Errorf("neither %q nor %q found", needle, cleanNeedle)
		}
	}

	start := strings.LastIndex(xml[:idx], "<w:p ")
	if start < 0 {
		return "", fmt.Errorf("no enclosing <w:p> before offset %d", idx)
	}
	openEnd := strings.Index(xml[start:], ">")
	if openEnd < 0 {
		return "", fmt.Errorf("unterminated <w:p> tag at offset %d", start)
	}
	openEnd += start + 1

	end := strings.Index(xml[idx:], "</w:p>")
	if end < 0 {
		return "", fmt.Errorf("no closing </w:p> after offset %d", idx)
	}
	end += idx

	return xml[:openEnd] + newBody + xml[end:], nil
}
