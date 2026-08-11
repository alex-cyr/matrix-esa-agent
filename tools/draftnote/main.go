// draftnote adds the {{DraftNote}} placeholder to
// knowledge/ESA_PHASE_I_Template.docx.
//
// The note reports an incomplete historical style baseline
// ("[DRAFT NOTE — INTERNAL: generated against N of M historical baselines —
// remove before issuance]") and must be impossible to miss, so it goes on the
// cover page. It is a drafting artifact for the EP to clear, NOT an ASTM data
// gap -- which is why it does not go anywhere near DataGaps_Text.
//
// The placeholder is added as a run inside the empty centred paragraph that
// already sits between the cover title and the "At <address>" block, rather
// than as a new paragraph of its own. That distinction matters: a new paragraph
// would shift the cover page down by a line on *every* report, including the
// ones with a complete baseline, and the note is supposed to cost nothing when
// there is nothing to report. Reusing an existing empty paragraph means the
// complete case is byte-for-byte unchanged in layout, because the run's empty
// <w:t> cannot make the line taller than the paragraph mark already does.
//
// Run from the repo root:
//
//	go run ./tools/draftnote
//
// Idempotent: re-running finds the placeholder already present.
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

const (
	templatePath = "knowledge/ESA_PHASE_I_Template.docx"
	documentPart = "word/document.xml"
	placeholder  = "{{DraftNote}}"
)

// Bold and dark red at 10pt, against a cover page that is otherwise navy
// Calibri Light at 18pt. It should look like something that does not belong.
const draftNoteRun = `<w:r><w:rPr><w:rFonts w:ascii="Calibri" w:hAnsi="Calibri" w:cs="Calibri"/>` +
	`<w:b/><w:color w:val="C00000"/><w:sz w:val="20"/><w:szCs w:val="20"/></w:rPr>` +
	`<w:t>` + placeholder + `</w:t></w:r>`

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
	changed := false

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

		if f.Name == documentPart {
			updated, err := insertPlaceholder(string(content))
			if err != nil {
				log.Fatalf("%s: %v", f.Name, err)
			}
			if updated != string(content) {
				changed = true
			}
			content = []byte(updated)
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

	if !changed {
		fmt.Println("no change — " + placeholder + " is already present")
		return
	}

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
	fmt.Printf("added %s to %s\n", placeholder, documentPart)
}

// insertPlaceholder puts the draft-note run into the empty paragraph directly
// above the cover page's "At <address>" block.
func insertPlaceholder(xml string) (string, error) {
	if strings.Contains(xml, placeholder) {
		return xml, nil
	}

	// Anchor on the cover's "At" run, which immediately precedes
	// {{SiteStreetAddress}}.
	at := strings.Index(xml, "<w:t>At</w:t>")
	if at < 0 {
		return "", fmt.Errorf(`cover anchor "<w:t>At</w:t>" not found`)
	}
	if addr := strings.Index(xml, "{{SiteStreetAddress}}"); addr < 0 || addr < at {
		return "", fmt.Errorf("cover anchor found but {{SiteStreetAddress}} does not follow it; template structure has changed")
	}

	atPara := strings.LastIndex(xml[:at], "<w:p ")
	if atPara < 0 {
		return "", fmt.Errorf("no enclosing paragraph for the cover anchor")
	}
	targetPara := strings.LastIndex(xml[:atPara], "<w:p ")
	if targetPara < 0 {
		return "", fmt.Errorf("no paragraph precedes the cover anchor paragraph")
	}

	end := strings.Index(xml[targetPara:], "</w:p>")
	if end < 0 {
		return "", fmt.Errorf("unterminated paragraph before the cover anchor")
	}
	end += targetPara

	// It must be an empty paragraph. Inserting into one that already has runs
	// would put the note on a line with real cover content.
	if strings.Contains(xml[targetPara:end], "<w:r>") || strings.Contains(xml[targetPara:end], "<w:r ") {
		return "", fmt.Errorf("the paragraph before the cover anchor is not empty; refusing to insert into cover content")
	}

	return xml[:end] + draftNoteRun + xml[end:], nil
}
