package core

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// IsProcessedDocxPart reports whether a zip entry is one of the parts the merge
// rewrites, and therefore one that must be re-validated afterwards.
func IsProcessedDocxPart(name string) bool {
	return name == "word/document.xml" ||
		strings.HasPrefix(name, "word/header") ||
		strings.HasPrefix(name, "word/footer")
}

// wordLineBreak is the run-internal markup Word uses for a line break inside a
// text run. Applied after escaping so its own angle brackets survive.
const wordLineBreak = `</w:t><w:br/><w:t xml:space="preserve">`

// escapeXMLText escapes the characters that are illegal in XML text content.
// Values land inside <w:t> text nodes, so quotes and apostrophes need no
// escaping. Ampersand must be replaced first or the others would double-escape.
func escapeXMLText(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// stripIllegalXMLChars removes code points XML 1.0 forbids outright. Tab, LF
// and CR are the only characters permitted below 0x20; unpaired surrogates and
// the non-characters U+FFFE/U+FFFF are rejected by conforming parsers.
func stripIllegalXMLChars(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t' || r == '\n' || r == '\r':
			return r
		case r < 0x20:
			return -1
		case r >= 0xD800 && r <= 0xDFFF:
			return -1
		case r == 0xFFFE || r == 0xFFFF:
			return -1
		}
		return r
	}, s)
}

// SanitizeDocxValue prepares a payload value for insertion into a Word text
// node.
//
// Order is deliberate: strip illegal code points, then XML-escape, then expand
// newlines into Word break markup. Escaping last would mangle the break markup;
// escaping first would leave control characters that no parser accepts.
//
// An unescaped "&" in a value such as "Diane & Brian J. Pete" or
// "12735 & 12725 Providence Road" produces a document Word refuses to open.
func SanitizeDocxValue(s string) string {
	s = stripIllegalXMLChars(s)
	s = escapeXMLText(s)
	// Normalize line endings first so CRLF does not emit two breaks.
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.ReplaceAll(s, "\n", wordLineBreak)
	return s
}

// ValidateDocxXML re-opens a written docx and parses every part the merge
// rewrote, checking both well-formedness and element balance.
//
// A document Word cannot open must never be reported as success, so callers
// should treat any error here as fatal to the request and must not publish the
// artifact.
func ValidateDocxXML(path string) error {
	r, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("post-merge validation: reopen %q: %w", path, err)
	}
	defer r.Close()

	checked := 0
	for _, f := range r.File {
		if !IsProcessedDocxPart(f.Name) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("post-merge validation: open %s: %w", f.Name, err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return fmt.Errorf("post-merge validation: read %s: %w", f.Name, err)
		}

		dec := xml.NewDecoder(bytes.NewReader(b))
		depth := 0
		for {
			tok, err := dec.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				return fmt.Errorf("post-merge validation: %s malformed at byte %d: %w",
					f.Name, dec.InputOffset(), err)
			}
			switch tok.(type) {
			case xml.StartElement:
				depth++
			case xml.EndElement:
				depth--
			}
		}
		if depth != 0 {
			return fmt.Errorf("post-merge validation: %s has unbalanced elements (final depth %d)", f.Name, depth)
		}
		checked++
	}

	if checked == 0 {
		return fmt.Errorf("post-merge validation: %q contains no word/document.xml", path)
	}
	return nil
}
